package loom

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"
)

func validateManifest(files map[string]fileDescriptor, tombstones map[string]time.Time, baseline map[string]string) error {
	if len(files) > maxSyncFiles || len(tombstones) > maxSyncFiles || len(baseline) > maxSyncFiles {
		return fmt.Errorf("too many sync entries")
	}
	var bytes int64
	for p, f := range files {
		if !validRelativePath(p) || f.Path != p || f.Size < 0 || f.Size > maxFileSize || !validSHA256(f.SHA256) {
			return fmt.Errorf("invalid file descriptor")
		}
		bytes += f.Size
	}
	if bytes > 1<<30 {
		return fmt.Errorf("folder exceeds the 1 GiB transfer limit")
	}
	for p := range tombstones {
		if !validRelativePath(p) {
			return fmt.Errorf("invalid deletion path")
		}
	}
	for p, h := range baseline {
		if !validRelativePath(p) || !validSHA256(h) {
			return fmt.Errorf("invalid sync baseline")
		}
	}
	return nil
}

func openSyncFile(root *os.Root, name string) (*os.File, error) {
	if !validRelativePath(name) {
		return nil, fmt.Errorf("invalid file path")
	}
	if err := ensureSafeParent(root, path.Dir(name)); err != nil {
		return nil, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return nil, fmt.Errorf("file is not a regular file within the size limit")
	}
	return root.Open(name)
}

func syncFileHash(root *os.Root, name string) (string, error) {
	f, err := openSyncFile(root, name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return "", err
	}
	if size > maxFileSize {
		return "", fmt.Errorf("file exceeds size limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Check the destination again at installation time so edits made during transfer survive.
func checkSyncDestination(root *os.Root, name string, expected *fileDescriptor) error {
	h, err := syncFileHash(root, name)
	if expected == nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("destination appeared during sync: %s", name)
	}
	if err != nil {
		return err
	}
	if h != expected.SHA256 {
		return fmt.Errorf("destination changed during sync: %s", name)
	}
	return nil
}

func fileChunkManifest(f *os.File, desc fileDescriptor) (*FileChunkManifest, error) {
	m := &FileChunkManifest{Path: desc.Path, Size: desc.Size, SHA256: desc.SHA256, ChunkSize: DefaultChunkSize}
	buf := make([]byte, DefaultChunkSize)
	full := sha256.New()
	for offset := int64(0); offset < desc.Size; offset += DefaultChunkSize {
		size := min(DefaultChunkSize, desc.Size-offset)
		n, err := f.ReadAt(buf[:size], offset)
		if err != nil || int64(n) != size {
			return nil, fmt.Errorf("source changed during sync")
		}
		h := sha256.Sum256(buf[:n])
		full.Write(buf[:n])
		m.Chunks = append(m.Chunks, ChunkInfo{Index: len(m.Chunks), Offset: offset, Size: size, SHA256: hex.EncodeToString(h[:])})
	}
	if hex.EncodeToString(full.Sum(nil)) != desc.SHA256 {
		return nil, fmt.Errorf("source changed during sync")
	}
	return m, nil
}

func validateChunkManifest(m *FileChunkManifest, desc fileDescriptor) error {
	if m.Size != desc.Size || m.SHA256 != desc.SHA256 || m.ChunkSize != DefaultChunkSize || int64(len(m.Chunks)) != (desc.Size+DefaultChunkSize-1)/DefaultChunkSize {
		return fmt.Errorf("invalid chunk manifest")
	}
	for i, c := range m.Chunks {
		offset := int64(i) * DefaultChunkSize
		if c.Index != i || c.Offset != offset || c.Size != min(DefaultChunkSize, desc.Size-offset) || !validSHA256(c.SHA256) {
			return fmt.Errorf("invalid chunk descriptor")
		}
	}
	return nil
}

func sendSyncFile(stream io.ReadWriter, root *os.Root, desc fileDescriptor) (int64, error) {
	f, err := openSyncFile(root, desc.Path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	hdr := TransferHeader{Path: desc.Path, Size: desc.Size, ModTime: desc.ModTime, SHA256: desc.SHA256, IsDelta: desc.Size >= DefaultChunkSize}
	// Hash from the same open file used to send; the receiver verifies the result too.
	m, err := fileChunkManifest(f, desc)
	if err != nil {
		return 0, err
	}
	if err := writeFrame(stream, mustJSON(hdr)); err != nil {
		return 0, err
	}
	var transferred int64
	if hdr.IsDelta {
		if err := writeFrame(stream, mustJSON(m)); err != nil {
			return 0, err
		}
		var needed NeededChunksReply
		if err := readFrame(stream, &needed, 64<<10); err != nil {
			return 0, err
		}
		seen := make(map[int]bool)
		for _, i := range needed.Needed {
			if i < 0 || i >= len(m.Chunks) || seen[i] {
				return transferred, fmt.Errorf("invalid requested chunk index")
			}
			seen[i] = true
			c := m.Chunks[i]
			buf := make([]byte, c.Size)
			if _, err := f.ReadAt(buf, c.Offset); err != nil {
				return transferred, err
			}
			if err := writeFrame(stream, mustJSON(ChunkFrame{Index: i, Size: c.Size})); err != nil {
				return transferred, err
			}
			n, err := stream.Write(buf)
			transferred += int64(n)
			if err != nil {
				return transferred, err
			}
		}
	} else {
		n, err := io.CopyN(stream, f, desc.Size)
		transferred += n
		if err != nil {
			return transferred, err
		}
	}
	var ack TransferAck
	if err := readFrame(stream, &ack, 16<<10); err != nil {
		return transferred, err
	}
	if !ack.OK {
		return transferred, fmt.Errorf("peer rejected file: %s", ack.Error)
	}
	return transferred, nil
}

// All reads, temporary writes and renames are rooted in the locally selected folder.
func receiveSyncFile(stream io.ReadWriter, root *os.Root, hdr TransferHeader, destination string, expected *fileDescriptor) (int64, error) {
	desc := fileDescriptor{Path: hdr.Path, Size: hdr.Size, SHA256: hdr.SHA256}
	fail := func(err error) (int64, error) {
		_ = writeFrame(stream, mustJSON(TransferAck{Error: err.Error()}))
		return 0, err
	}
	if !validRelativePath(destination) || validateManifest(map[string]fileDescriptor{hdr.Path: desc}, nil, nil) != nil {
		return fail(fmt.Errorf("invalid transfer header"))
	}
	if err := ensureSafeParent(root, path.Dir(destination)); err != nil {
		return fail(err)
	}
	if err := checkSyncDestination(root, destination, expected); err != nil {
		return fail(err)
	}
	if err := CheckFreeDiskSpace(root.Name(), hdr.Size); err != nil {
		return fail(err)
	}
	suffix, err := randomSuffix()
	if err != nil {
		return fail(err)
	}
	tmpName := path.Join(path.Dir(destination), ".loom-part-"+suffix)
	tmp, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return fail(err)
	}
	defer tmp.Close()
	defer root.Remove(tmpName)
	h := sha256.New()
	output := io.MultiWriter(tmp, h)
	var transferred int64
	if hdr.IsDelta {
		var m FileChunkManifest
		if err := readFrame(stream, &m, 2<<20); err != nil {
			return fail(err)
		}
		if err := validateChunkManifest(&m, desc); err != nil {
			return fail(err)
		}
		// Seed verified matching chunks into the temporary file, avoiding unbounded buffering.
		existing, _ := openSyncFile(root, destination)
		if existing != nil {
			defer existing.Close()
		}
		needed := make([]int, 0)
		buf := make([]byte, DefaultChunkSize)
		for _, c := range m.Chunks {
			matches := false
			if existing != nil {
				n, err := existing.ReadAt(buf[:c.Size], c.Offset)
				if err == nil && int64(n) == c.Size {
					hash := sha256.Sum256(buf[:n])
					matches = hex.EncodeToString(hash[:]) == c.SHA256
				}
			}
			if matches {
				if _, err := tmp.WriteAt(buf[:c.Size], c.Offset); err != nil {
					return fail(err)
				}
			} else {
				needed = append(needed, c.Index)
			}
		}
		if err := writeFrame(stream, mustJSON(NeededChunksReply{Needed: needed})); err != nil {
			return 0, err
		}
		for _, i := range needed {
			var frame ChunkFrame
			if err := readFrame(stream, &frame, 16<<10); err != nil {
				return 0, err
			}
			c := m.Chunks[i]
			if frame.Index != i || frame.Size != c.Size {
				return fail(fmt.Errorf("invalid chunk frame"))
			}
			if _, err := io.ReadFull(stream, buf[:c.Size]); err != nil {
				return 0, err
			}
			hash := sha256.Sum256(buf[:c.Size])
			if hex.EncodeToString(hash[:]) != c.SHA256 {
				return fail(fmt.Errorf("chunk checksum mismatch"))
			}
			if _, err := tmp.WriteAt(buf[:c.Size], c.Offset); err != nil {
				return fail(err)
			}
			transferred += c.Size
		}
		if _, err := tmp.Seek(0, 0); err != nil {
			return fail(err)
		}
		if _, err := io.Copy(h, tmp); err != nil {
			return fail(err)
		}
	} else {
		n, err := io.CopyN(output, stream, hdr.Size)
		transferred = n
		if err != nil {
			return fail(err)
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != hdr.SHA256 {
		return fail(fmt.Errorf("file checksum mismatch"))
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := checkSyncDestination(root, destination, expected); err != nil {
		return fail(err)
	}
	if err := root.Rename(tmpName, destination); err != nil {
		return fail(err)
	}
	// Chtimes through os.Root is racy on Unix. Transfer correctness uses content hashes.
	if err := writeFrame(stream, mustJSON(TransferAck{OK: true})); err != nil {
		return transferred, err
	}
	return transferred, nil
}
