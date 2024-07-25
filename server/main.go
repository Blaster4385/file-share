package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	_ "github.com/lib/pq"
)

const (
	maxUploadSize = 3 * 1024 * 1024 * 1024 // 3 GB
	keySize       = 32
	nonceSize     = 12
)

var db *sql.DB
var port string

//go:embed all:dist
var dist embed.FS

func registerHandlers(e *echo.Echo) {
	e.Use(middleware.BodyLimit(fmt.Sprintf("%dM", maxUploadSize/(1024*1024))))
	e.Use(middleware.StaticWithConfig(middleware.StaticConfig{
		Root:       "dist",
		Index:      "index.html",
		HTML5:      true,
		Filesystem: http.FS(dist),
	}))
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())
	e.POST("/upload_chunk", handleUploadChunk)
	e.POST("/upload_complete", handleUploadComplete)
	e.GET("/download/:id", handleDownload)
	e.GET("/get/:id", handleGetFileInfo)
}

func main() {
	flag.StringVar(&port, "port", "8080", "HTTP server port")
	flag.Parse()
	var err error
	db, err = initDB()
	if err != nil {
		panic(err)
	}
	defer db.Close()

	e := echo.New()
	registerHandlers(e)

	startCleanupScheduler()

	e.Logger.Fatal(e.Start(":" + port))
}

func initDB() (*sql.DB, error) {
	user := os.Getenv("POSTGRES_USER")
	password := os.Getenv("POSTGRES_PASSWORD")
	dbname := os.Getenv("POSTGRES_DB")

	dbURL := fmt.Sprintf("postgres://%s:%s@localhost/%s?sslmode=disable", user, password, dbname)
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := createTables(ctx, db); err != nil {
		return nil, err
	}

	return db, nil
}

func createTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS chunks (
            upload_id TEXT,
            chunk_index INT,
            chunk_data BYTEA,
            created_at TIMESTAMPTZ DEFAULT NOW(),
            PRIMARY KEY (upload_id, chunk_index)
        );
        CREATE TABLE IF NOT EXISTS files (
            id TEXT,
            name TEXT,
            chunk_index INT,
            chunk_data BYTEA,
            created_at TIMESTAMPTZ DEFAULT NOW(),
            PRIMARY KEY (id, chunk_index)
        );
    `)
	return err
}

func handleUploadChunk(c echo.Context) error {
	uploadId := c.FormValue("uploadId")
	chunkIndex, err := strconv.Atoi(c.FormValue("chunkIndex"))
	if err != nil {
		return handleError(c, fmt.Errorf("invalid chunk index: %v", err), http.StatusBadRequest)
	}
	chunk, err := c.FormFile("chunk")
	if err != nil {
		return handleError(c, fmt.Errorf("error getting form file: %v", err), http.StatusBadRequest)
	}

	src, err := chunk.Open()
	if err != nil {
		return handleError(c, fmt.Errorf("error opening chunk: %v", err), http.StatusInternalServerError)
	}
	defer src.Close()

	chunkData, err := io.ReadAll(src)
	if err != nil {
		return handleError(c, fmt.Errorf("error reading chunk data: %v", err), http.StatusInternalServerError)
	}

	if err := storeChunkInDB(c.Request().Context(), uploadId, chunkIndex, chunkData); err != nil {
		return handleError(c, fmt.Errorf("error storing chunk in database: %v", err), http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusOK)
}

func storeChunkInDB(ctx context.Context, uploadId string, chunkIndex int, chunkData []byte) error {
	_, err := db.ExecContext(ctx, "INSERT INTO chunks (upload_id, chunk_index, chunk_data, created_at) VALUES ($1, $2, $3, NOW())", uploadId, chunkIndex, chunkData)
	return err
}

func handleUploadComplete(c echo.Context) error {
	uploadId := c.FormValue("uploadId")
	chunkCount, err := strconv.Atoi(c.FormValue("chunkCount"))
	if err != nil {
		return handleError(c, fmt.Errorf("invalid chunk count: %v", err), http.StatusBadRequest)
	}
	fileName := c.FormValue("fileName")

	key, err := generateRandomKey()
	if err != nil {
		return handleError(c, fmt.Errorf("error generating encryption key: %v", err), http.StatusInternalServerError)
	}

	id := generateID()
	for i := 0; i < chunkCount; i++ {
		chunkData, err := getChunkFromDB(c.Request().Context(), uploadId, i)
		if err != nil {
			return handleError(c, fmt.Errorf("error retrieving chunk data: %v", err), http.StatusInternalServerError)
		}

		encryptedData, err := encryptFile(bytes.NewReader(chunkData), key)
		if err != nil {
			return handleError(c, fmt.Errorf("error encrypting chunk: %v", err), http.StatusInternalServerError)
		}

		if err := storeChunkInFilesTable(c.Request().Context(), id, fileName, i, encryptedData); err != nil {
			return handleError(c, fmt.Errorf("error storing chunk in database: %v", err), http.StatusInternalServerError)
		}
	}

	encodedKey := hex.EncodeToString(key)
	response := struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}{
		ID:  id,
		Key: encodedKey,
	}

	return c.JSON(http.StatusOK, response)
}

func storeChunkInFilesTable(ctx context.Context, id, fileName string, chunkIndex int, encryptedData []byte) error {
	_, err := db.ExecContext(ctx, "INSERT INTO files (id, name, chunk_index, chunk_data, created_at) VALUES ($1, $2, $3, $4, NOW())", id, fileName, chunkIndex, encryptedData)
	return err
}

func getChunkFromDB(ctx context.Context, uploadId string, chunkIndex int) ([]byte, error) {
	var chunkData []byte
	err := db.QueryRowContext(ctx, "SELECT chunk_data FROM chunks WHERE upload_id = $1 AND chunk_index = $2", uploadId, chunkIndex).Scan(&chunkData)
	return chunkData, err
}

func handleDownload(c echo.Context) error {
	id := c.Param("id")
	keyHex := c.QueryParam("key")

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return handleError(c, fmt.Errorf("invalid key: %v", err), http.StatusBadRequest)
	}

	fileName, err := getFileNameFromDB(c.Request().Context(), id)
	if err != nil {
		return handleError(c, fmt.Errorf("error getting file name from database: %v", err), http.StatusInternalServerError)
	}

	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))

	err = decryptAndStreamChunks(c.Response(), id, key)
	if err != nil {
		return handleError(c, fmt.Errorf("error decrypting and streaming file: %v", err), http.StatusInternalServerError)
	}

	return nil
}

func getFileNameFromDB(ctx context.Context, id string) (fileName string, err error) {
	err = db.QueryRowContext(ctx, "SELECT name FROM files WHERE id = $1 LIMIT 1", id).Scan(&fileName)
	if err == sql.ErrNoRows {
		return "", errors.New("file not found")
	}
	return fileName, err
}

func decryptAndStreamChunks(w io.Writer, id string, key []byte) error {
	rows, err := db.Query("SELECT chunk_data FROM files WHERE id = $1 ORDER BY chunk_index", id)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var encryptedData []byte
		if err := rows.Scan(&encryptedData); err != nil {
			return err
		}

		plaintext, err := decryptFile(encryptedData, key)
		if err != nil {
			return err
		}

		_, err = w.Write(plaintext)
		if err != nil {
			return err
		}
	}

	return rows.Err()
}

func handleGetFileInfo(c echo.Context) error {
	id := c.Param("id")
	keyHex := c.QueryParam("key")

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return handleError(c, fmt.Errorf("invalid key: %v", err), http.StatusBadRequest)
	}

	fileName, err := getFileNameFromDB(c.Request().Context(), id)
	if err != nil {
		return handleError(c, fmt.Errorf("error getting file name from database: %v", err), http.StatusInternalServerError)
	}

	fileSize, err := getTotalFileSize(id, key)
	if err != nil {
		return handleError(c, fmt.Errorf("error getting file size: %v", err), http.StatusInternalServerError)
	}

	fileInfo := struct {
		FileName string `json:"fileName"`
		FileSize string `json:"fileSize"`
	}{
		FileName: fileName,
		FileSize: fileSize,
	}

	return c.JSON(http.StatusOK, fileInfo)
}

func getTotalFileSize(id string, key []byte) (string, error) {
	var totalSize int64
	rows, err := db.Query("SELECT chunk_data FROM files WHERE id = $1 ORDER BY chunk_index", id)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	for rows.Next() {
		var encryptedData []byte
		if err := rows.Scan(&encryptedData); err != nil {
			return "", err
		}

		plaintext, err := decryptFile(encryptedData, key)
		if err != nil {
			return "", err
		}

		totalSize += int64(len(plaintext))
	}

	var fileSize string
	if totalSize >= 1024*1024 {
		fileSize = fmt.Sprintf("%.2f MB", float64(totalSize)/(1024*1024))
	} else {
		fileSize = fmt.Sprintf("%.2f KB", float64(totalSize)/1024)
	}

	return fileSize, rows.Err()
}

func handleError(c echo.Context, err error, status int) error {
	fmt.Printf("error: %v\n", err)
	return c.JSON(status, map[string]string{"error": err.Error()})
}

func generateRandomKey() ([]byte, error) {
	key := make([]byte, keySize)
	_, err := rand.Read(key)
	return key, err
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func encryptFile(plaintext io.Reader, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintextBytes, err := io.ReadAll(plaintext)
	if err != nil {
		return nil, err
	}

	ciphertext := aesgcm.Seal(nonce, nonce, plaintextBytes, nil)
	return ciphertext, nil
}

func decryptFile(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

func startCleanupScheduler() {
	ticker := time.NewTicker(24 * time.Hour)
	go func() {
		for range ticker.C {
			cleanupChunks()
		}
	}()
}

func cleanupChunks() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	_, err := db.ExecContext(ctx, "DELETE FROM chunks WHERE created_at < NOW() - INTERVAL '1 day'")
	if err != nil {
		fmt.Printf("error cleaning up chunks: %v\n", err)
	}
}
