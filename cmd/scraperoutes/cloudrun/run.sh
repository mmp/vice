#!/bin/bash
# Fetch routes for scraperoutes from a Cloud Run job's tasks, each working
# through its share of the queue from its own instance with the usual delay
# between requests, and record them in resources/scraped-routes.json:
#   cmd/scraperoutes/cloudrun/run.sh [scraperoutes flags]
# The flags are the usual ones for choosing what to fetch, e.g.
# "-mincount 50 -delay 15s"; -limit defaults to TASKS*PER_TASK
# pairs rather than 25.
#
# The queue is decided locally, where the flight data is, and handed to the
# tasks through Cloud Storage; each task leaves what it fetched there, and
# that is merged into the database locally at the end, even if some tasks
# failed. The project, artifact repository, bucket and service account are
# wxingest's (see cmd/wxingest/cloudrun/setup.sh). Run only one of these at a
# time.
set -ex

PROJECT=${PROJECT:-vice-464116} # not necessarily the gcloud default project
REGION=${REGION:-us-west1}
IMAGE=$REGION-docker.pkg.dev/$PROJECT/vice/scraperoutes:latest
SA=ingest-wx@$PROJECT.iam.gserviceaccount.com
TASKS=${TASKS:-20}
PER_TASK=${PER_TASK:-100}
RUN=gs://vice-wx/scraperoutes/$(date -u +%Y%m%d-%H%M%S)
WORK=$(mktemp -d -t scraperoutes.XXXXXX)

cd "$(git rev-parse --show-toplevel)"

docker buildx build --platform linux/amd64 -f cmd/scraperoutes/cloudrun/Dockerfile -t $IMAGE --push .

# A later -limit in "$@" overrides this one.
go run ./cmd/scraperoutes -limit=$((TASKS * PER_TASK)) "$@" -plan=$WORK
gcloud storage cp $WORK/pairs.json $RUN/pairs.json

gcloud run jobs deploy scraperoutes --image=$IMAGE --region=$REGION --project=$PROJECT \
    --service-account=$SA --memory=1Gi --cpu=1 --task-timeout=24h --max-retries=1

# A task that fails has still saved what it fetched before it did, so merge
# whatever there is and report the job's failure afterward.
status=0
gcloud run jobs execute scraperoutes --region=$REGION --project=$PROJECT --wait \
    --tasks=$TASKS --args=-worker=$RUN || status=$?

gcloud storage cp -r $RUN/results $WORK/
go run ./cmd/scraperoutes "$@" -merge=$WORK
gcloud storage rm -r $RUN
rm -r $WORK

exit $status
