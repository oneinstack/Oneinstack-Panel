package panelbackup

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const (
	backupMagic         = "ONEINSTACK-PANEL-BACKUP-V1\n"
	encryptionChunkSize = 1 << 20
	maxHeaderBytes      = 16 << 10
)

type encryptionHeader struct {
	SchemaVersion int       `json:"schemaVersion"`
	KDF           string    `json:"kdf"`
	Salt          string    `json:"salt"`
	N             int       `json:"n"`
	R             int       `json:"r"`
	P             int       `json:"p"`
	Cipher        string    `json:"cipher"`
	NoncePrefix   string    `json:"noncePrefix"`
	ChunkSize     int       `json:"chunkSize"`
	CreatedAt     time.Time `json:"createdAt"`
}

func validatePassphrase(passphrase string) error {
	if !utf8.ValidString(passphrase) || strings.ContainsRune(passphrase, '\x00') {
		return ErrInvalidPassphrase
	}
	if len([]byte(passphrase)) < 12 || len([]byte(passphrase)) > 256 {
		return fmt.Errorf("%w: passphrase must contain 12-256 bytes", ErrInvalidPassphrase)
	}
	return nil
}

func encryptArchive(sourcePath, destinationPath, passphrase string, createdAt time.Time) error {
	if err := validatePassphrase(passphrase); err != nil {
		return err
	}
	salt := make([]byte, 16)
	noncePrefix := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate backup encryption salt: %w", err)
	}
	if _, err := rand.Read(noncePrefix); err != nil {
		return fmt.Errorf("generate backup nonce prefix: %w", err)
	}
	header := encryptionHeader{
		SchemaVersion: EncryptionSchema,
		KDF:           "scrypt", Salt: base64.StdEncoding.EncodeToString(salt),
		N: 1 << 15, R: 8, P: 1,
		Cipher:      "XChaCha20-Poly1305",
		NoncePrefix: base64.StdEncoding.EncodeToString(noncePrefix),
		ChunkSize:   encryptionChunkSize, CreatedAt: createdAt.UTC(),
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, header.N, header.R, header.P, chacha20poly1305.KeySize)
	if err != nil {
		return fmt.Errorf("derive panel backup encryption key: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	clear(key)
	if err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	removeDestination := true
	defer func() {
		destination.Close()
		if removeDestination {
			_ = os.Remove(destinationPath)
		}
	}()
	if _, err := destination.WriteString(backupMagic); err != nil {
		return err
	}
	if len(headerBytes) > maxHeaderBytes {
		return ErrInvalidBackup
	}
	if err := binary.Write(destination, binary.BigEndian, uint32(len(headerBytes))); err != nil {
		return err
	}
	if _, err := destination.Write(headerBytes); err != nil {
		return err
	}
	buffer := make([]byte, encryptionChunkSize)
	var index uint64
	for {
		count, readErr := io.ReadFull(source, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		if count == 0 {
			break
		}
		nonce := chunkNonce(noncePrefix, index)
		ciphertext := aead.Seal(nil, nonce, buffer[:count], chunkAAD(headerBytes, index))
		if err := binary.Write(destination, binary.BigEndian, uint32(count)); err != nil {
			return err
		}
		if _, err := destination.Write(ciphertext); err != nil {
			return err
		}
		index++
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}
	if err := binary.Write(destination, binary.BigEndian, uint32(0)); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	removeDestination = false
	return syncDirectory(filepath.Dir(destinationPath))
}

func decryptArchive(sourcePath, destinationPath, passphrase string, maxPlaintextBytes int64) (encryptionHeader, error) {
	if err := validatePassphrase(passphrase); err != nil {
		return encryptionHeader{}, err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return encryptionHeader{}, err
	}
	defer source.Close()
	reader := bufio.NewReader(source)
	magic := make([]byte, len(backupMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != backupMagic {
		return encryptionHeader{}, ErrInvalidBackup
	}
	var headerSize uint32
	if err := binary.Read(reader, binary.BigEndian, &headerSize); err != nil ||
		headerSize == 0 || headerSize > maxHeaderBytes {
		return encryptionHeader{}, ErrInvalidBackup
	}
	headerBytes := make([]byte, int(headerSize))
	if _, err := io.ReadFull(reader, headerBytes); err != nil {
		return encryptionHeader{}, ErrInvalidBackup
	}
	var header encryptionHeader
	decoder := json.NewDecoder(bytes.NewReader(headerBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&header); err != nil {
		return encryptionHeader{}, ErrInvalidBackup
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return encryptionHeader{}, err
	}
	if header.SchemaVersion != EncryptionSchema || header.KDF != "scrypt" ||
		header.N != 1<<15 || header.R != 8 || header.P != 1 ||
		header.Cipher != "XChaCha20-Poly1305" || header.ChunkSize != encryptionChunkSize ||
		header.CreatedAt.IsZero() {
		return encryptionHeader{}, ErrInvalidBackup
	}
	salt, err := base64.StdEncoding.DecodeString(header.Salt)
	if err != nil || len(salt) != 16 {
		return encryptionHeader{}, ErrInvalidBackup
	}
	noncePrefix, err := base64.StdEncoding.DecodeString(header.NoncePrefix)
	if err != nil || len(noncePrefix) != 16 {
		return encryptionHeader{}, ErrInvalidBackup
	}
	key, err := scrypt.Key([]byte(passphrase), salt, header.N, header.R, header.P, chacha20poly1305.KeySize)
	if err != nil {
		return encryptionHeader{}, err
	}
	aead, err := chacha20poly1305.NewX(key)
	clear(key)
	if err != nil {
		return encryptionHeader{}, err
	}
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return encryptionHeader{}, err
	}
	removeDestination := true
	defer func() {
		destination.Close()
		if removeDestination {
			_ = os.Remove(destinationPath)
		}
	}()
	var (
		index uint64
		total int64
	)
	for {
		var plaintextSize uint32
		if err := binary.Read(reader, binary.BigEndian, &plaintextSize); err != nil {
			return encryptionHeader{}, ErrInvalidBackup
		}
		if plaintextSize == 0 {
			break
		}
		if plaintextSize > uint32(header.ChunkSize) ||
			total+int64(plaintextSize) > maxPlaintextBytes {
			return encryptionHeader{}, ErrInvalidBackup
		}
		ciphertext := make([]byte, int(plaintextSize)+aead.Overhead())
		if _, err := io.ReadFull(reader, ciphertext); err != nil {
			return encryptionHeader{}, ErrInvalidBackup
		}
		plaintext, err := aead.Open(nil, chunkNonce(noncePrefix, index), ciphertext, chunkAAD(headerBytes, index))
		if err != nil {
			return encryptionHeader{}, ErrInvalidPassphrase
		}
		if _, err := destination.Write(plaintext); err != nil {
			return encryptionHeader{}, err
		}
		total += int64(len(plaintext))
		index++
	}
	if _, err := reader.Peek(1); err != io.EOF {
		return encryptionHeader{}, ErrInvalidBackup
	}
	if err := destination.Sync(); err != nil {
		return encryptionHeader{}, err
	}
	if err := destination.Close(); err != nil {
		return encryptionHeader{}, err
	}
	removeDestination = false
	return header, nil
}

func chunkNonce(prefix []byte, index uint64) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	copy(nonce, prefix)
	binary.BigEndian.PutUint64(nonce[len(prefix):], index)
	return nonce
}

func chunkAAD(header []byte, index uint64) []byte {
	aad := make([]byte, len(header)+8)
	copy(aad, header)
	binary.BigEndian.PutUint64(aad[len(header):], index)
	return aad
}

func readEncryptionHeader(path string) (encryptionHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return encryptionHeader{}, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	magic := make([]byte, len(backupMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != backupMagic {
		return encryptionHeader{}, ErrInvalidBackup
	}
	var size uint32
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil ||
		size == 0 || size > maxHeaderBytes {
		return encryptionHeader{}, ErrInvalidBackup
	}
	content := make([]byte, int(size))
	if _, err := io.ReadFull(reader, content); err != nil {
		return encryptionHeader{}, ErrInvalidBackup
	}
	var header encryptionHeader
	if err := json.Unmarshal(content, &header); err != nil ||
		header.SchemaVersion != EncryptionSchema || header.CreatedAt.IsZero() {
		return encryptionHeader{}, ErrInvalidBackup
	}
	return header, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidBackup
	}
	return nil
}
