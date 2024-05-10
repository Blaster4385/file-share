package main

import (
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
	db = initDB()
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

func initDB() *sql.DB {
	db, err := sql.Open("postgres", "postgres://file:password@localhost/filedb?sslmode=disable")
	if err != nil {
		panic(err)
	}
	if err := createFilesTable(db); err != nil {
		panic(err)
	}
	return db
}

func createFilesTable(db *sql.DB) error {
	_, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS files (
            id TEXT PRIMARY KEY,
            name TEXT,
            data BYTEA
        );
    `)
	return err
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(maxUploadSize)

	key, err := generateRandomKey()
	if err != nil {
		handleError(w, err, http.StatusInternalServerError)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		handleError(w, errors.New("error parsing uploaded file"), http.StatusBadRequest)
		return
	}
	defer file.Close()

	id := generateID()

	encryptedData, err := encryptFile(file, key)
	if err != nil {
		handleError(w, errors.New("error encrypting file"), http.StatusInternalServerError)
		return
	}

	err = storeFileInDB(id, handler.Filename, encryptedData)
	if err != nil {
		handleError(w, errors.New("error storing file in database"), http.StatusInternalServerError)
		return
	}

	encodedKey := hex.EncodeToString(key)
	url := fmt.Sprintf("/download/%s?key=%s", id, encodedKey)
	fmt.Fprintf(w, "File uploaded successfully. Download URL: %s", url)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	keyHex := r.URL.Query().Get("key")
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		handleError(w, errors.New("invalid key"), http.StatusBadRequest)
		return
	}

	fileName, encryptedData, err := getFileFromDB(id)
	if err != nil {
		handleError(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))

	plaintext, err := decryptFile(encryptedData, key)
	if err != nil {
		handleError(w, errors.New("error decrypting file"), http.StatusInternalServerError)
		return
	}

	if _, err := w.Write(plaintext); err != nil {
		handleError(w, errors.New("error writing response"), http.StatusInternalServerError)
		return
	}
}

func handleGetFileInfo(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	keyHex := r.URL.Query().Get("key")
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		handleError(w, errors.New("invalid key"), http.StatusBadRequest)
		return
	}

	fileName, encryptedData, err := getFileFromDB(id)
	if err != nil {
		handleError(w, err, http.StatusInternalServerError)
		return
	}

	plaintext, err := decryptFile(encryptedData, key)
	if err != nil {
		handleError(w, errors.New("error decrypting file"), http.StatusInternalServerError)
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

func storeFileInDB(id, fileName string, encryptedData []byte) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec("INSERT INTO files (id, name, data) VALUES ($1, $2, $3)", id, fileName, encryptedData)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func getFileFromDB(id string) (fileName string, encryptedData []byte, err error) {
	err = db.QueryRow("SELECT name, data FROM files WHERE id = $1", id).Scan(&fileName, &encryptedData)
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

func decryptFile(encryptedData []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(encryptedData) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := encryptedData[:nonceSize], encryptedData[nonceSize:]
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
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
