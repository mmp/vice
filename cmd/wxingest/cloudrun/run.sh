#!/bin/bash
# Build and push the wxingest image, then run a full ingest as Cloud Run
# jobs, in dependency order:
#   1. metar and tfr, as a single task (atmos needs the METAR manifest)
#   2. precip, fanned out across tasks that shard scrape/WX by object hash
#   3. precip manifest (atmos needs it to know which hours have precip)
#   4. atmos, fanned out across tasks that shard the missing hours
#   5. atmos-avg repair, for any grid whose average didn't get written
#   6. atmos manifest
#   7. atmos-series rollup, which is what packaging reads
# and then package resources/wx locally from the results.
# Requires a gcloud recent enough to support --tasks/--args overrides on
# "gcloud run jobs execute". Run only one of these at a time.
set -ex

PROJECT=${PROJECT:-vice-464116} # not necessarily the gcloud default project
REGION=${REGION:-us-west1} # must match the vice-wx bucket's location
IMAGE=$REGION-docker.pkg.dev/$PROJECT/vice/wxingest:latest
SA=ingest-wx@$PROJECT.iam.gserviceaccount.com
PRECIP_TASKS=${PRECIP_TASKS:-20}
ATMOS_TASKS=${ATMOS_TASKS:-16} # consider more for deep backfills
# A no-op unless grids are missing their average; raise it well above this to
# backfill a large gap, since each missing average costs a grid download.
AVG_TASKS=${AVG_TASKS:-8}

cd "$(git rev-parse --show-toplevel)"

# What to ingest and package is decided by the airports and facilities that
# have scenarios, but the image can't load the scenarios itself: doing so
# validates each one's video map against the .mappack files, which are far too
# big to ship. Generate the list here, bake it into the image below, and hand
# the same file to wxpackage at the end so that the two agree even if the
# scenarios change while the ingest is running.
FACILITIES=cmd/wxingest/cloudrun/facilities.json
go run ./cmd/viceserver -wxfacilities=$FACILITIES

docker buildx build --platform linux/amd64 -f cmd/wxingest/cloudrun/Dockerfile -t $IMAGE --push .

# 4Gi covers METAR's rebuild-from-archive fallback; precip tasks need less.
gcloud run jobs deploy wxingest-precip --image=$IMAGE --region=$REGION --project=$PROJECT \
    --service-account=$SA --memory=4Gi --cpu=2 --task-timeout=2h --max-retries=1

# Peak atmos memory is ~3GB plus the HRRR download in tmpfs; GOMEMLIMIT
# leaves the GC headroom.
gcloud run jobs deploy wxingest-atmos --image=$IMAGE --region=$REGION --project=$PROJECT \
    --service-account=$SA --memory=8Gi --cpu=4 --task-timeout=6h --max-retries=1 \
    --set-env-vars=GOMEMLIMIT=6GiB

gcloud run jobs execute wxingest-precip --region=$REGION --project=$PROJECT --wait \
    --tasks=1 --args=metar,tfr

gcloud run jobs execute wxingest-precip --region=$REGION --project=$PROJECT --wait \
    --tasks=$PRECIP_TASKS --args=-nworkers=64,precip

gcloud run jobs execute wxingest-precip --region=$REGION --project=$PROJECT --wait \
    --tasks=1 --args=-manifests-only,precip

gcloud run jobs execute wxingest-atmos --region=$REGION --project=$PROJECT --wait \
    --tasks=$ATMOS_TASKS --args=atmos

gcloud run jobs execute wxingest-atmos --region=$REGION --project=$PROJECT --wait \
    --tasks=$AVG_TASKS --args=atmosavg

gcloud run jobs execute wxingest-atmos --region=$REGION --project=$PROJECT --wait \
    --tasks=1 --args=-manifests-only,atmos

# Gather each facility's hourly averages into one object. Reading the hourly
# objects individually is fine in-region and hopeless from outside it: there
# are a few hundred thousand of them and each costs a round trip.
gcloud run jobs execute wxingest-atmos --region=$REGION --project=$PROJECT --wait \
    --tasks=1 --args=-nworkers=64,atmosseries

# Packaging runs locally: one object per facility, ~40MB in total, rather
# than the ~700GiB of grids they were distilled from. It writes straight
# into the checkout.
go run ./cmd/wxpackage -output=resources/wx -facilities=$FACILITIES
