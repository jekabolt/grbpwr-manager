# Analytics credentials

BigQuery and GA4 use the same Google Cloud service account JSON key.

1. Copy `ga4.json.example` to `ga4.json`:
   ```bash
   cp ga4.json.example ga4.json
   ```

2. In [Google Cloud Console](https://console.cloud.google.com/iam-admin/serviceaccounts):
   - Create a service account (or use existing)
   - Grant roles: **BigQuery Data Viewer**, **BigQuery Job User**, **Analytics Data API Viewer**
   - Create a JSON key and download it

3. Replace the contents of `ga4.json` with the downloaded key file.

`ga4.json` is gitignored and must not be committed.
