package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	"github.com/rs/cors"
)

const (
	maxUploadSize = 10 * 1024 * 1024 // 10 MB
	keySize       = 32
	nonceSize     = 12
)

var db *sql.DB

func main() {
	var err error
	db, err = initDB()
	if err != nil {
		panic(err)
	}
	defer db.Close()

	router := mux.NewRouter()
	router.HandleFunc("/upload", handleUpload).Methods("POST")
	router.HandleFunc("/download/{id}", handleDownload).Methods("GET")
	router.HandleFunc("/get/{id}", handleGetFileInfo).Methods("GET")

	handler := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"*"},
	}).Handler(router)

	http.ListenAndServe(":8080", handler)
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

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		handleError(w, fmt.Errorf("error parsing multipart form: %v", err), http.StatusBadRequest)
		return
	}

	key, err := generateRandomKey()
	if err != nil {
		handleError(w, fmt.Errorf("error generating encryption key: %v", err), http.StatusInternalServerError)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		handleError(w, fmt.Errorf("error getting form file: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	id := generateID()

	encryptedData, err := encryptFile(file, key)
	if err != nil {
		handleError(w, fmt.Errorf("error encrypting file: %v", err), http.StatusInternalServerError)
		return
	}

	if err := storeFileInDB(r.Context(), id, handler.Filename, encryptedData); err != nil {
		handleError(w, fmt.Errorf("error storing file in database: %v", err), http.StatusInternalServerError)
		return
	}

	encodedKey := hex.EncodeToString(key)
	url := fmt.Sprintf("/download/%s?key=%s", id, encodedKey)
	fmt.Fprintf(w, "File uploaded successfully. Download URL: %s", url)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	keyHex := r.URL.Query().Get("key")

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		handleError(w, fmt.Errorf("invalid key: %v", err), http.StatusBadRequest)
		return
	}

	fileName, encryptedData, err := getFileFromDB(r.Context(), id)
	if err != nil {
		handleError(w, fmt.Errorf("error getting file from database: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))

	err = decryptAndStreamFile(w, encryptedData, key)
	if err != nil {
		handleError(w, fmt.Errorf("error decrypting and streaming file: %v", err), http.StatusInternalServerError)
		return
	}
}

func handleGetFileInfo(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	keyHex := r.URL.Query().Get("key")

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		handleError(w, fmt.Errorf("invalid key: %v", err), http.StatusBadRequest)
		return
	}

	fileName, encryptedData, err := getFileFromDB(r.Context(), id)
	if err != nil {
		handleError(w, fmt.Errorf("error getting file from database: %v", err), http.StatusInternalServerError)
		return
	}

	plaintext, err := decryptFile(encryptedData, key)
	if err != nil {
		handleError(w, fmt.Errorf("error decrypting file: %v", err), http.StatusInternalServerError)
		return
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fileInfo)
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

func handleError(w http.ResponseWriter, err error, code int) {
	http.Error(w, err.Error(), code)
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
