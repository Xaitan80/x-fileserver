package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/xaitan80/learn-file-storage-s3-golang-starter/internal/auth"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	}

	// JWT auth
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

	// Parse form
	const maxMemory = 10 << 20 // 10MB
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to parse form", err)
		return
	}

	file, fileHeader, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Missing thumbnail file", err)
		return
	}
	defer file.Close()

	// Get video
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized,
			"Not the owner of this video",
			fmt.Errorf("user %s does not own video", userID))
		return
	}

	// Determine file extension
	mediaType := fileHeader.Header.Get("Content-Type")
	var ext string
	switch mediaType {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	default:
		ext = ".bin"
	}

	// Build file path in assets dir
	filePath := filepath.Join(cfg.assetsRoot, fmt.Sprintf("%s%s", videoID.String(), ext))

	// Create file on disk
	outFile, err := os.Create(filePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create file", err)
		return
	}
	defer outFile.Close()

	// Copy bytes to file
	_, err = io.Copy(outFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to save file", err)
		return
	}

	// Update ThumbnailURL to point to served /assets file
	url := fmt.Sprintf("http://localhost:%s/assets/%s%s", cfg.port, videoID.String(), ext)
	video.ThumbnailURL = &url

	// Save to DB
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
