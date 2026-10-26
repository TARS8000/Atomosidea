# Anchored Summary

## Objective
- Fix static site thumbnail upload so new thumbnails display correctly instead of old ones or black screen
- Eliminate blob URL corruption causing `ERR_FILE_NOT_FOUND` errors

## Important Details
- SFSP worker scans thumbnails with ClamAV+YARA and publishes a completion event; `static-site-worker` (`processStaticSiteThumbnail`) updates `thumbnail_url` only for a clean image and clears `thumbnail_sfsp_job_id` either way, so a rejection keeps the old URL
- Frontend poll logic previously masked rejections (`accepted: !!site.thumbnail_url`) and preview used local blob URL instead of server URL
- Blob URL cache-buster `?t=` corrupted blob URLs → `ERR_FILE_NOT_FOUND` → premature navigation showing old thumbnail
- nginx `/static-sites/thumbnails/` location block (`frontend/nginx.conf`, `location ~ ^/static-sites/thumbnails/(.+)$`) proxies to the `static_site_storage` upstream with anti-cache headers; the block was deployed in the running `atmosidea-frontend` container when verified earlier (via `docker exec atmosidea-frontend grep`)

## Work State
### Completed
- Verified the nginx block was deployed in the running container config (not a deployment issue)
- Fixed `pollStaticSite`: `accepted = site.thumbnail_url !== existingThumbnailUrl` (rejects when URL unchanged)
- Fixed `handleSubmit`: uses `result.thumbnailUrl` (server URL) on success, reverts to `existingThumbnailUrl` on rejection
- Removed fragile reveal/preload logic that appended `?t=` to blob URLs
- TypeScript typecheck passes

### Active
- (none)

### Blocked
- (none)

## Next Move
1. Test thumbnail upload end-to-end: verify new thumbnail appears on edit page, MyPage, and after reload
2. If user reports old thumbnail still shows, add debug logging to poll/handleSubmit to capture job_id transitions

## Relevant Files
- `frontend/src/pages/EditStaticSitePage.tsx`: poll logic (lines 51-87), handleSubmit restructured (lines 89-170)
- `frontend/nginx.conf`: `/static-sites/thumbnails/` proxy block (`location ~ ^/static-sites/thumbnails/(.+)$`)
- `backend/static-site-service/static-site-worker/main.go`: thumbnail processing (`handleThumbnailMessage`, `processStaticSiteThumbnail`, `commitThumbnail`)
- `backend/security/sfsp/internal/worker/worker.go`: image scan (ClamAV+YARA; `scanFile`, `determineOverallStatus`)