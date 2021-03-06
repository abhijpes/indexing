package memdb

import "os"
import "bufio"
import "errors"
import "github.com/couchbase/indexing/secondary/fdb"
import "bytes"

const DiskBlockSize = 4 * 1024 // 4K is ok for page cache writes

var (
	ErrNotEnoughSpace = errors.New("Not enough space in the buffer")
	forestdbConfig    *forestdb.Config
)

func init() {
	forestdbConfig = forestdb.DefaultConfig()
	forestdbConfig.SetSeqTreeOpt(forestdb.SEQTREE_NOT_USE)
	forestdbConfig.SetBufferCacheSize(1024 * 1024)

}

type FileWriter interface {
	Open(path string) error
	WriteItem(*Item) error
	Checksum() uint32
	Close() error
}

type FileReader interface {
	Open(path string) error
	ReadItem() (*Item, error)
	Checksum() uint32
	Close() error
}

func (m *MemDB) newFileWriter(t FileType) FileWriter {
	var w FileWriter
	if t == RawdbFile {
		w = &rawFileWriter{db: m}
	} else if t == ForestdbFile {
		w = &forestdbFileWriter{db: m}
	}
	return w
}

func (m *MemDB) newFileReader(t FileType, ver int) FileReader {
	var r FileReader
	if t == RawdbFile {
		r = &rawFileReader{db: m, version: ver}
	} else if t == ForestdbFile {
		r = &forestdbFileReader{db: m}
	}
	return r
}

type rawFileWriter struct {
	db       *MemDB
	fd       *os.File
	w        *bufio.Writer
	buf      []byte
	path     string
	checksum uint32
}

func (f *rawFileWriter) Open(path string) error {
	var err error
	f.fd, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0755)
	if err == nil {
		f.buf = make([]byte, encodeBufSize)
		f.w = bufio.NewWriterSize(f.fd, DiskBlockSize)
	}
	return err
}

func (f *rawFileWriter) WriteItem(itm *Item) error {
	checksum, err := f.db.EncodeItem(itm, f.buf, f.w)
	f.checksum = f.checksum ^ checksum
	return err
}

func (f *rawFileWriter) Checksum() uint32 {
	return f.checksum
}

func (f *rawFileWriter) Close() error {
	terminator := &Item{}

	if err := f.WriteItem(terminator); err != nil {
		return err
	}

	f.w.Flush()
	return f.fd.Close()
}

type rawFileReader struct {
	version  int
	db       *MemDB
	fd       *os.File
	r        *bufio.Reader
	buf      []byte
	path     string
	checksum uint32
}

func (f *rawFileReader) Open(path string) error {
	var err error
	f.fd, err = os.Open(path)
	if err == nil {
		f.buf = make([]byte, encodeBufSize)
		f.r = bufio.NewReaderSize(f.fd, DiskBlockSize)
	}
	return err
}

func (f *rawFileReader) ReadItem() (*Item, error) {
	itm, checksum, err := f.db.DecodeItem(f.version, f.buf, f.r)
	if itm != nil { // Checksum excludes terminal nil item
		f.checksum = f.checksum ^ checksum
	}
	return itm, err
}

func (f *rawFileReader) Checksum() uint32 {
	return f.checksum
}

func (f *rawFileReader) Close() error {
	return f.fd.Close()
}

type forestdbFileWriter struct {
	db       *MemDB
	file     *forestdb.File
	store    *forestdb.KVStore
	buf      []byte
	wbuf     bytes.Buffer
	checksum uint32
}

func (f *forestdbFileWriter) Open(path string) error {
	var err error
	f.file, err = forestdb.Open(path, forestdbConfig)
	if err == nil {
		f.buf = make([]byte, encodeBufSize)
		f.store, err = f.file.OpenKVStoreDefault(nil)
	}

	return err
}

func (f *forestdbFileWriter) WriteItem(itm *Item) error {
	f.wbuf.Reset()
	checksum, err := f.db.EncodeItem(itm, f.buf, &f.wbuf)
	if err == nil {
		f.checksum = checksum
		err = f.store.SetKV(f.wbuf.Bytes(), nil)
	}

	return err
}

func (f *forestdbFileWriter) Checksum() uint32 {
	return f.checksum
}

func (f *forestdbFileWriter) Close() error {
	err := f.file.Commit(forestdb.COMMIT_NORMAL)
	if err == nil {
		err = f.store.Close()
		if err == nil {
			err = f.file.Close()
		}
	}

	return err
}

type forestdbFileReader struct {
	db       *MemDB
	file     *forestdb.File
	store    *forestdb.KVStore
	iter     *forestdb.Iterator
	buf      []byte
	checksum uint32
}

func (f *forestdbFileReader) Open(path string) error {
	var err error

	f.file, err = forestdb.Open(path, forestdbConfig)
	if err == nil {
		f.buf = make([]byte, encodeBufSize)
		f.store, err = f.file.OpenKVStoreDefault(nil)
		if err == nil {
			f.iter, err = f.store.IteratorInit(nil, nil, forestdb.ITR_NONE)
		}
	}

	return err
}

func (f *forestdbFileReader) ReadItem() (*Item, error) {
	itm := &Item{}
	doc, err := f.iter.Get()
	if err == forestdb.FDB_RESULT_ITERATOR_FAIL {
		return nil, nil
	}

	f.iter.Next()
	if err == nil {
		rbuf := bytes.NewBuffer(doc.Key())
		itm, f.checksum, err = f.db.DecodeItem(0, f.buf, rbuf)
	}

	return itm, err
}

func (f *forestdbFileReader) Checksum() uint32 {
	return f.checksum
}

func (f *forestdbFileReader) Close() error {
	f.iter.Close()
	f.store.Close()
	return f.file.Close()
}
