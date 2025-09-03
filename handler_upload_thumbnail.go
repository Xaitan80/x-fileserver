package main

import (
    "encoding/base64"
    "fmt"
    "io"
    "net/http"

    "github.com/google/uuid"
    "github.com/xaitan80/learn-file-storage-s3-golang-starter/internal/auth"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	// Parse videoID from URL
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	}

	// JWT authentication (already implemented)
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// --- Parse the form ---
	const maxMemory = 10 << 20 // 10MB
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to parse form", err)
		return
	}

	// Get the uploaded file
	file, fileHeader, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Missing thumbnail file", err)
		return
	}
	defer file.Close()

	// Read all image data
	data, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to read file", err)
		return
	}

	// Get the media type from file header
	mediaType := fileHeader.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	// --- Get video metadata from database ---
	video, err := cfg.db.GetVideo(videoID)

	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}

	// Check ownership
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not the owner of this video", fmt.Errorf("user %s does not own video", userID))
		return
	}

    // --- Encode thumbnail to base64 and build a data URL ---
    // Convert raw file bytes to a base64 string. This avoids storing binary data directly
    // and allows the client to display the image without making a separate request.
    encoded := base64.StdEncoding.EncodeToString(data)
    dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, encoded)

    // Update video metadata with the data URL. The database schema includes a
    // `thumbnail_url` column that should contain the full data URI. Because the field
    // is a pointer in the Video struct, take its address here.
    video.ThumbnailURL = &dataURL

    // Update the record in the database with the new ThumbnailURL
    err = cfg.db.UpdateVideo(video)
    if err != nil {
        respondWithError(w, http.StatusInternalServerError, "Failed to update video", err)
        return
    }

    // Respond with updated video JSON
    respondWithJSON(w, http.StatusOK, video)
}
