package main

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/xaitan80/x-fileserver/internal/auth"
	"github.com/xaitan80/x-fileserver/internal/database"
)

// Create a new video draft (title + description only, no files yet)
func (cfg *apiConfig) handlerVideosCreate(w http.ResponseWriter, r *http.Request) {
	// Authenticate
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

	// Parse request body
	var params struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Insert new video record
	video, err := cfg.db.CreateVideo(database.CreateVideoParams{
		UserID:      userID,
		Title:       params.Title,
		Description: params.Description,
	})

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create video", err)
		return
	}

	respondWithJSON(w, http.StatusCreated, video)
}

// Get a single video by ID
func (cfg *apiConfig) handlerVideoGet(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}

	// 🔑 convert DB video to signed URL before sending to client
	signedVideo, err := cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to sign video URL", err)
		return
	}

	respondWithJSON(w, http.StatusOK, signedVideo)
}

// Get all videos for the authenticated user
func (cfg *apiConfig) handlerVideosRetrieve(w http.ResponseWriter, r *http.Request) {
	// Authenticate user
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

	// Fetch videos for this user
	videos, err := cfg.db.GetVideos(userID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to get videos", err)
		return
	}

	// Sign URLs
	signedVideos := make([]database.Video, 0, len(videos))
	for _, v := range videos {
		signedVideo, err := cfg.dbVideoToSignedVideo(v)
		if err != nil {
			respondWithError(w, http.StatusInternalServerError, "Failed to sign video URL", err)
			return
		}
		signedVideos = append(signedVideos, signedVideo)
	}

	respondWithJSON(w, http.StatusOK, signedVideos)
}

// Delete a video by ID
func (cfg *apiConfig) handlerVideoDelete(w http.ResponseWriter, r *http.Request) {
	// Authenticate
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

	// Parse video ID
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	}

	// Fetch the video to check ownership
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not the owner of this video", nil)
		return
	}

	// Delete it
	if err := cfg.db.DeleteVideo(videoID); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to delete video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
