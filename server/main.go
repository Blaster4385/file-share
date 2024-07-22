package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	e.POST("/upload", handleUpload)
	e.GET("/download/:id", handleDownload)
	e.GET("/get/:id", handleGetFileInfo)
}

func main() {
	var err error
	db, err = initDB()
	if err != nil {
		panic(err)
	}
	defer db.Close()

	e := echo.New()
	registerHandlers(e)
	e.Logger.Fatal(e.Start(":8080"))
}

func initDB() (*sql.DB, error) {
	db, err := sql.Open("postgres", "postgres://file:password@localhost/filedb?sslmode=disable")
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := createFilesTable(ctx, db); err != nil {
		return nil, err
	}

	return db, nil
}

func createFilesTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS files (
            id TEXT PRIMARY KEY,
            name TEXT,
            data BYTEA
        );
    `)
	return err
}

func handleUpload(c echo.Context) error {
	r := c.Request()
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		return handleError(c, fmt.Errorf("error parsing multipart form: %v", err), http.StatusBadRequest)
	}

	key, err := generateRandomKey()
	if err != nil {
		return handleError(c, fmt.Errorf("error generating encryption key: %v", err), http.StatusInternalServerError)
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		return handleError(c, fmt.Errorf("error getting form file: %v", err), http.StatusBadRequest)
	}
	defer file.Close()

	id := generateID()

	encryptedData, err := encryptFile(file, key)
	if err != nil {
		return handleError(c, fmt.Errorf("error encrypting file: %v", err), http.StatusInternalServerError)
	}

	if err := storeFileInDB(r.Context(), id, handler.Filename, encryptedData); err != nil {
		return handleError(c, fmt.Errorf("error storing file in database: %v", err), http.StatusInternalServerError)
	}

	encodedKey := hex.EncodeToString(key)

	type UploadResponse struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}

	response := UploadResponse{
		ID:  id,
		Key: encodedKey,
	}

	return c.JSON(http.StatusOK, response)
}

func handleDownload(c echo.Context) error {
	id := c.Param("id")
	keyHex := c.QueryParam("key")

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return handleError(c, fmt.Errorf("invalid key: %v", err), http.StatusBadRequest)
	}

	fileName, encryptedData, err := getFileFromDB(c.Request().Context(), id)
	if err != nil {
		return handleError(c, fmt.Errorf("error getting file from database: %v", err), http.StatusInternalServerError)
	}

	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))

	err = decryptAndStreamFile(c.Response(), encryptedData, key)
	if err != nil {
		return handleError(c, fmt.Errorf("error decrypting and streaming file: %v", err), http.StatusInternalServerError)
	}

	return nil
}

func handleGetFileInfo(c echo.Context) error {
	id := c.Param("id")
	keyHex := c.QueryParam("key")

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return handleError(c, fmt.Errorf("invalid key: %v", err), http.StatusBadRequest)
	}

	fileName, encryptedData, err := getFileFromDB(c.Request().Context(), id)
	if err != nil {
		return handleError(c, fmt.Errorf("error getting file from database: %v", err), http.StatusInternalServerError)
	}

	plaintext, err := decryptFile(encryptedData, key)
	if err != nil {
		return handleError(c, fmt.Errorf("error decrypting file: %v", err), http.StatusInternalServerError)
	}

	fileSizeBytes := len(plaintext)
	var fileSize string
	if fileSizeBytes >= 1024*1024 {
		fileSize = fmt.Sprintf("%.2f MB", float64(fileSizeBytes)/(1024*1024))
	} else {
		fileSize = fmt.Sprintf("%.2f KB", float64(fileSizeBytes)/1024)
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

func storeFileInDB(ctx context.Context, id, fileName string, encryptedData []byte) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, "INSERT INTO files (id, name, data) VALUES ($1, $2, $3)", id, fileName, encryptedData)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func getFileFromDB(ctx context.Context, id string) (fileName string, encryptedData []byte, err error) {
	err = db.QueryRowContext(ctx, "SELECT name, data FROM files WHERE id = $1", id).Scan(&fileName, &encryptedData)
	if err == sql.ErrNoRows {
		return "", nil, errors.New("file not found")
	}
	return fileName, encryptedData, err
}

func handleError(c echo.Context, err error, code int) error {
	return c.JSON(code, map[string]string{"error": err.Error()})
}

func encryptFile(in io.Reader, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	plaintext, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}

	ciphertext := aesgcm.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ciphertext...), nil
}

func decryptAndStreamFile(w io.Writer, encryptedData []byte, key []byte) error {
	if len(encryptedData) < nonceSize {
		return errors.New("ciphertext too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	nonce, ciphertext := encryptedData[:nonceSize], encryptedData[nonceSize:]
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return err
	}

	_, err = w.Write(plaintext)
	return err
}

func decryptFile(encryptedData []byte, key []byte) ([]byte, error) {
	if len(encryptedData) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce, ciphertext := encryptedData[:nonceSize], encryptedData[nonceSize:]
	return aesgcm.Open(nil, nonce, ciphertext, nil)
}

func generateRandomKey() ([]byte, error) {
	key := make([]byte, keySize)
	_, err := rand.Read(key)
	return key, err
}

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
