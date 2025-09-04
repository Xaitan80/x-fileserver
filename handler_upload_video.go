package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/xaitan80/x-fileserver/internal/auth"
)

type ffprobeOutput struct {
	Streams []struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
}

// getVideoAspectRatio runs ffprobe on a local file and returns "16:9", "9:16", or "other"
func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		filePath,
	)

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe failed: %w", err)
	}

	var probe ffprobeOutput
	if err := json.Unmarshal(out.Bytes(), &probe); err != nil {
		return "", fmt.Errorf("unmarshal failed: %w", err)
	}

	if len(probe.Streams) == 0 {
		return "other", nil
	}

	width := probe.Streams[0].Width
	height := probe.Streams[0].Height
	if width == 0 || height == 0 {
		return "other", nil
	}

	ratio := float64(width) / float64(height)
	if ratio > 1.7 && ratio < 1.8 {
		return "16:9", nil
	} else if ratio > 0.55 && ratio < 0.6 {
		return "9:16", nil
	}
	return "other", nil
}

// processVideoForFastStart tries remux first (dropping data streams), then re-encode if needed
func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".faststart.mp4"

	// Attempt 1: remux (copy video/audio, drop data streams like tmcd)
	cmd := exec.Command(
		"ffmpeg",
		"-i", filePath,
		"-map", "0:v",
		"-map", "0:a?",
		"-c", "copy",
		"-movflags", "faststart",
		outputPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err == nil {
		return outputPath, nil
	} else {
		fmt.Printf("ffmpeg remux failed, retrying with re-encode: %s\n", stderr.String())
	}

	// Fallback: re-encode (square pixels), copy audio
	outputPathReencode := filePath + ".reencode.mp4"
	cmd = exec.Command(
		"ffmpeg",
		"-i", filePath,
		"-vf", "setsar=1",
		"-c:v", "libx264", "-crf", "18", "-preset", "veryfast",
		"-c:a", "copy",
		"-movflags", "faststart",
		outputPathReencode,
	)

	stderr.Reset()
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg re-encode failed: %v, details: %s", err, stderr.String())
	}

	return outputPathReencode, nil
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// Limit upload size to 1 GB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

	// Parse videoID
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	}

	// Auth
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

	// Lookup video
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

	// Parse uploaded file
	const maxMemory = 10 << 20
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		respondWithError(w, http.StatusBadRequest, "Failed to parse form", err)
		return
	}

	file, fileHeader, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Missing video file", err)
		return
	}
	defer file.Close()

	// Validate MIME type
	contentType := fileHeader.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Unsupported video type", nil)
		return
	}

	// Save to temp file
	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err = io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to save temp file", err)
		return
	}

	// Process video for fast start
	processedPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to process video for fast start", err)
		return
	}
	defer os.Remove(processedPath)

	processedFile, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to open processed file", err)
		return
	}
	defer processedFile.Close()

	// Determine aspect ratio (for folder prefix)
	aspect, err := getVideoAspectRatio(processedPath)
	if err != nil {
		aspect = "other"
	}

	var prefix string
	switch aspect {
	case "16:9":
		prefix = "landscape/"
	case "9:16":
		prefix = "portrait/"
	default:
		prefix = "other/"
	}

	// Generate random filename
	randomBytes := make([]byte, 32)
	if _, err = rand.Read(randomBytes); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate random key", err)
		return
	}
	randomName := base64.RawURLEncoding.EncodeToString(randomBytes)
	key := prefix + randomName + filepath.Ext(fileHeader.Filename)

	// Upload to S3
	_, err = cfg.s3Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &key,
		Body:        processedFile,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload video to S3", err)
		return
	}

	// Store a CloudFront URL (not presigned, not bucket,key)
	// Expect cfg.s3CfDistribution to be something like: dxxxxxxx.cloudfront.net
	cfURL := fmt.Sprintf("https://%s/%s", cfg.s3CfDistribution, key)
	video.VideoURL = &cfURL

	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video record", err)
		return
	}

	// Return the DB record as-is (no signing needed anymore)
	respondWithJSON(w, http.StatusOK, video)
}
