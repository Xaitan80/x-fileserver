package main

// This file is intentionally left blank. The `/api/thumbnails/{videoID}` endpoint
// has been removed, and thumbnail data is stored directly in the Video's
// ThumbnailURL field as a data URI. We keep this file to avoid build errors
// when updating existing projects that might still reference it.