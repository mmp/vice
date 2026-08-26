#!/bin/bash
# One-time GCP setup for running wxingest as Cloud Run jobs; requires gcloud
# authenticated with sufficient permissions on the project.
# The jobs themselves are created/updated by run.sh, which must be kept in
# sync with the project, region, and service account chosen here.
set -ex

PROJECT=${PROJECT:-vice-464116} # not necessarily the gcloud default project
# The region should match the vice-wx bucket's location:
#   gcloud storage buckets describe gs://vice-wx --format="value(location)"
REGION=${REGION:-us-west1}
SA=ingest-wx@$PROJECT.iam.gserviceaccount.com

gcloud services enable run.googleapis.com artifactregistry.googleapis.com --project=$PROJECT

gcloud artifacts repositories create vice --repository-format=docker \
    --location=$REGION --project=$PROJECT

gcloud auth configure-docker $REGION-docker.pkg.dev

gcloud iam service-accounts create ingest-wx --project=$PROJECT

# The jobs run with this service account and use its credentials (rather
# than VICE_GCS_CREDENTIALS) to access the bucket.
gcloud storage buckets add-iam-policy-binding gs://vice-wx \
    --member=serviceAccount:$SA --role=roles/storage.objectAdmin
