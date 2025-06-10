package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"chunk-file/pkg/action"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

type FileDto struct {
	Key         string `json:"key" form:"key"`
	FileName    string `json:"fileName" form:"fileName"`
	CountChunks int    `json:"countChunks" form:"countChunks"`
}

const CHUNK_SIZE = 2 * 1024 * 1024 // 2mb

type Presigner struct {
	PresignClient *s3.PresignClient
}

func init() {
	action.LoadS3Client()
}

// PutObject makes a presigned request that can be used to put an object in a bucket.
// The presigned request is valid for the specified number of seconds.
func (presigner Presigner) PutObject(
	ctx context.Context, bucketName string, objectKey string, lifetimeSecs int64) (*v4.PresignedHTTPRequest, error) {
	request, err := presigner.PresignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(lifetimeSecs * int64(time.Second))
	})
	if err != nil {
		log.Printf("Couldn't get a presigned request to put %v:%v. Here's why: %v\n",
			bucketName, objectKey, err)
	}
	return request, err
}

func main() {
	// Create upload directory if it doesn't exist
	err := os.MkdirAll("./tmp/chunks", 0755)
	if err != nil {
		fmt.Printf("Failed to create directory: %v\n", err)
		return
	}

	app := fiber.New(
		fiber.Config{
			BodyLimit:                    100 * 1024 * 1024, // 100MB
			StreamRequestBody:            true,
			DisablePreParseMultipartForm: true,
		},
	)
	app.Use(cors.New())
	app.Use(logger.New(logger.Config{
		Format:     "${time} | ${status} | ${latency} | ${method} | ${path}\n",
		TimeFormat: "2006-01-02T15:04:05Z07:00",
		TimeZone:   "Asia/Tokyo",
	}))

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Hello, World!")
	})

	app.Post("/upload", func(c *fiber.Ctx) error {
		file, err := c.FormFile("file")
		if err != nil {
			fmt.Println(err)
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "File not found"})
		}

		key := c.FormValue("key")
		fileName := c.FormValue("fileName")
		if key == "" || fileName == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing key or fileName"})
		}

		err = os.MkdirAll(fmt.Sprintf("./tmp/chunks/%s", key), 0755)
		if err != nil {
			fmt.Printf("Failed to create directory: %v\n", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create directory"})
		}

		err = c.SaveFile(file, fmt.Sprintf("./tmp/chunks/%s/%s", key, fileName))
		if err != nil {
			fmt.Println(err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save file"})
		}

		return c.JSON(fiber.Map{"success": true})
	})

	app.Post("/merge", func(c *fiber.Ctx) error {
		key := c.FormValue("key")
		originalFileName := c.FormValue("fileName")
		if key == "" || originalFileName == "" {
			fmt.Printf("Error: Missing parameters - key: %s, fileName: %s\n", key, originalFileName)
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing key or fileName"})
		}

		// err = os.MkdirAll(fmt.Sprintf("./tmp/merged/%s", key), 0755)
		// if err != nil {
		// 	fmt.Printf("Failed to create directory: %v\n", err)
		// 	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create directory"})
		// }

		// Get all chunks for this file
		pattern := fmt.Sprintf("./tmp/chunks/%s/%s-*", key, originalFileName)
		chunks, err := filepath.Glob(pattern)
		if err != nil {
			fmt.Printf("Error listing chunks: %v\nPattern used: %s\n", err, pattern)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to list chunks"})
		}

		if len(chunks) == 0 {
			fmt.Printf("No chunks found for pattern: %s\n", pattern)
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "No chunks found"})
		}

		fmt.Printf("Found %d chunks for file: %s\n", len(chunks), originalFileName)

		// Sort chunks by their index
		sort.Slice(chunks, func(i, j int) bool {
			iIndex := strings.Split(chunks[i], "-")
			jIndex := strings.Split(chunks[j], "-")
			return iIndex[len(iIndex)-1] < jIndex[len(jIndex)-1]
		})

		// Create the final file
		outputPath := fmt.Sprintf("./tmp/merged/%s", originalFileName)
		err = os.MkdirAll("./tmp/merged", 0755)
		if err != nil {
			fmt.Printf("Error creating merged directory: %v\n", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create merged directory"})
		}

		outFile, err := os.Create(outputPath)
		if err != nil {
			fmt.Printf("Error creating output file: %v\nPath: %s\n", err, outputPath)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create output file"})
		}
		defer outFile.Close()

		// Merge all chunks
		totalBytes := int64(0)
		for i, chunk := range chunks {
			chunkFile, err := os.Open(chunk)
			if err != nil {
				fmt.Printf("Error opening chunk %d/%d: %v\nChunk path: %s\n", i+1, len(chunks), err, chunk)
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to open chunk"})
			}

			written, err := io.Copy(outFile, chunkFile)
			totalBytes += written
			chunkFile.Close()
			if err != nil {
				fmt.Printf("Error merging chunk %d/%d: %v\nChunk path: %s\nBytes written: %d\n", i+1, len(chunks), err, chunk, written)
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to merge chunk"})
			}

			// // Delete the chunk after merging
			// if err := os.Remove(chunk); err != nil {
			// 	fmt.Printf("Warning: Failed to delete chunk %s: %v\n", chunk, err)
			// }
		}

		fmt.Printf("Successfully merged file: %s\nTotal chunks: %d\nTotal bytes: %d\nOutput path: %s\n",
			originalFileName, len(chunks), totalBytes, outputPath)

		repoS3 := action.GetRepoS3()

		url, err := repoS3.PutObject("my-bucket", "upload", 60*60)
		if err != nil {
			fmt.Printf("Error uploading file to S3: %v\n", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to upload file to S3"})
		}
		fmt.Println("\nUploaded file to S3: %s\n", url.URL)

		// 6. Upload merged file to Minio via presigned URL
		mergedFile, err := os.Open(outputPath)
		if err != nil {
			fmt.Printf("Error opening merged file: %v\n", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to open merged file"})
		}
		defer mergedFile.Close()

		req, err := http.NewRequest("PUT", url.URL, mergedFile)
		if err != nil {
			fmt.Printf("Error creating upload request: %v\n", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create upload request"})
		}

		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = totalBytes
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("Error uploading file to S3: %v\n", err)

			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to upload file to Minio"})
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("Error uploading file to S3: %v\n", resp.StatusCode)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to upload file to Minio 20000"})
		}

		return c.JSON(fiber.Map{
			"success": true,
			"path":    outputPath,
			"size":    totalBytes,
		})
	})

	app.Listen(":8080")
}
