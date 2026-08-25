#!/bin/bash
# Build and push the wxingest image, then run a full ingest as Cloud Run
# jobs, in dependency order:
#   1. metar and tfr, as a single task (atmos needs the METAR manifest)
#   2. precip, fanned out across tasks that shard scrape/WX by object hash
#   3. precip manifests (atmos needs the consolidated precip manifest)
#   4. atmos, fanned out across tasks that shard the missing hours
#   5. atmos manifest
# Requires a gcloud recent enough to support --tasks/--args overrides on
# "gcloud run jobs execute". Run only one of these at a time.
set -ex

PROJECT=${PROJECT:-$(gcloud config get-value project)}
REGION=${REGION:-us-central1} # must match the vice-wx bucket's location
IMAGE=$REGION-docker.pkg.dev/$PROJECT/vice/wxingest:latest
SA=wxingest@$PROJECT.iam.gserviceaccount.com
PRECIP_TASKS=${PRECIP_TASKS:-20}
ATMOS_TASKS=${ATMOS_TASKS:-16} # consider more for deep backfills

cd "$(git rev-parse --show-toplevel)"

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
    --tasks=1 --args=-manifests-only,atmos
