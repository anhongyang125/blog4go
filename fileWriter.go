// Copyright (c) 2015, huangjunwei <huangjunwei@youmi.net>. All rights reserved.

package blog4go

import (
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// ByteSize is type of sizes
type ByteSize int64

const (
	// unit of sizes

	_ = iota // ignore first value by assigning to blank identifier
	// KB unit of kilobyte
	KB ByteSize = 1 << (10 * iota)
	// MB unit of megabyte
	MB
	// GB unit of gigabyte
	GB

	// default logrotate condition

	// DefaultRotateSize is default size when size base logrotate needed
	DefaultRotateSize = 500 * MB
	// DefaultRotateLines is default lines when lines base logrotate needed
	DefaultRotateLines = 2000000 // 2 million
)

// baseFileWriter defines a writer for single file.
// It suppurts partially write while formatting message, logging level filtering,
// logrotate, user defined hook for every logging action, change configuration
// on the fly and logging with colors.
type baseFileWriter struct {
	// configuration about file
	// full path of the file
	fileName string
	// the file object
	file *os.File

	// the BLog
	blog *BLog

	// close sign, default false
	// set this tag true if writer is closed
	closed bool

	// configuration about logrotate
	// exclusive lock use in logrotate
	rotateLock *sync.Mutex

	// configuration about time base logrotate
	// sign of time base logrotate, default false
	// set this tag true if logrotate in time base mode
	timeRotated bool
	// signal send when time base rotate needed
	timeRotateSig chan bool

	// configuration about size && line base logrotate
	// sign of line base logrotate, default false
	// set this tag true if logrotate in line base mode
	lineRotated bool
	// line base logrotate threshold
	rotateLines int
	// total lines written from last size && line base logrotate
	currentLines int
	// sign of size base logrotate, default false
	// set this tag true if logrotate in size base mode
	sizeRotated bool
	// size rotate按行数、大小rotate, 后缀 xxx.1, xxx.2
	// signal send when size && line base logrotate
	sizeRotateSig chan bool
	// size base logrotate threshold
	rotateSize ByteSize
	// total size written after last size && line logrotate
	currentSize ByteSize
	// times of size && line base logrotate executions
	sizeRotateTimes int
	// channel used to sum up sizes written from last logrotate
	logSizeChan chan int

	// sign decided logging with colors or not, default false
	colored bool
}

// NewbaseFileWriter create a single file writer instance and return the poionter
// of it. When any errors happened during creation, a null writer and appropriate
// will be returned.
// fileName must be an absolute path to the destination log file
func newBaseFileWriter(fileName string) (fileWriter *baseFileWriter, err error) {
	fileWriter = new(baseFileWriter)
	fileWriter.fileName = fileName
	// open file target file
	file, err := os.OpenFile(fileName, os.O_WRONLY|os.O_APPEND|os.O_CREATE, os.FileMode(0644))
	fileWriter.file = file
	if nil != err {
		return fileWriter, err
	}
	fileWriter.blog = NewBLog(file)

	fileWriter.closed = false

	// about logrotate
	fileWriter.rotateLock = new(sync.Mutex)
	fileWriter.timeRotated = false
	fileWriter.timeRotateSig = make(chan bool)
	fileWriter.sizeRotateSig = make(chan bool)
	fileWriter.logSizeChan = make(chan int, 4096)

	fileWriter.lineRotated = false
	fileWriter.rotateSize = DefaultRotateSize
	fileWriter.currentSize = 0

	fileWriter.sizeRotated = false
	fileWriter.rotateLines = DefaultRotateLines
	fileWriter.currentLines = 0

	fileWriter.colored = false

	go fileWriter.daemon()

	return fileWriter, nil
}

// NewFileWriter initialize a file writer
// baseDir must be base directory of log files
func NewFileWriter(baseDir string) (err error) {
	singltonLock.Lock()
	defer singltonLock.Unlock()
	if nil != blog {
		return
	}

	fileWriter := new(MultiWriter)
	fileWriter.level = DEBUG
	fileWriter.closed = false

	fileWriter.writers = make(map[Level]Writer)
	for _, level := range Levels {
		fileName := fmt.Sprintf("%s.log", strings.ToLower(level.String()))
		writer, err := newBaseFileWriter(path.Join(baseDir, fileName))
		if nil != err {
			return err
		}
		fileWriter.writers[level] = writer
	}

	// log hook
	fileWriter.hook = nil
	fileWriter.hookLevel = DEBUG

	blog = fileWriter
	return
}

// daemon run in background as NewbaseFileWriter called.
// It flushes writer buffer every 10 seconds.
// It decides whether a time base when logrotate is needed.
// It sums up lines && sizes already written. Alse it does the lines &&
// size base logrotate
func (writer *baseFileWriter) daemon() {
	// tick every seconds
	// time base logrotate
	t := time.Tick(1 * time.Second)
	// tick every 10 seconds
	// auto flush writer buffer
	f := time.Tick(10 * time.Second)

DaemonLoop:
	for {
		select {
		case <-f:
			if writer.closed {
				break DaemonLoop
			}

			writer.blog.flush()
		case <-t:
			if writer.closed {
				break DaemonLoop
			}

			writer.rotateLock.Lock()

			date := time.Now().Format(DateFormat)

			if writer.timeRotated && date != timeCache.date {
				// need time base logrotate
				writer.sizeRotateTimes = 0

				fileName := fmt.Sprintf("%s.%s", writer.fileName, timeCache.dateYesterday)
				file, _ := os.OpenFile(fileName, os.O_WRONLY|os.O_APPEND|os.O_CREATE, os.FileMode(0644))

				writer.file.Close()
				writer.blog.resetFile(file)
				writer.file = file
			}

			writer.rotateLock.Unlock()
		// analyse lines && size written
		// do lines && size base logrotate
		case size := <-writer.logSizeChan:
			if writer.closed {
				break DaemonLoop
			}

			if !writer.sizeRotated && !writer.lineRotated {
				continue
			}

			writer.rotateLock.Lock()

			writer.currentSize += ByteSize(size)
			writer.currentLines++

			if (writer.sizeRotated && writer.currentSize > writer.rotateSize) || (writer.lineRotated && writer.currentLines > writer.rotateLines) {
				// need lines && size base logrotate
				writer.sizeRotateTimes++
				writer.currentSize = 0
				writer.currentLines = 0

				fileName := fmt.Sprintf("%s.%d", writer.fileName, writer.sizeRotateTimes+1)
				if writer.timeRotated {
					fileName = fmt.Sprintf("%s.%s.%d", writer.fileName, timeCache.date, writer.sizeRotateTimes+1)
				}
				file, _ := os.OpenFile(fileName, os.O_WRONLY|os.O_APPEND|os.O_CREATE, os.FileMode(0644))

				writer.file.Close()
				writer.blog.resetFile(file)
				writer.file = file
			}
			writer.rotateLock.Unlock()
		}
	}
}

// write writes pure message with specific level
func (writer *baseFileWriter) write(level Level, format string) {
	var size = 0
	defer func() {
		// logrotate
		if writer.sizeRotated || writer.lineRotated {
			writer.logSizeChan <- size
		}
	}()

	if writer.closed {
		return
	}

	size = writer.blog.write(level, format)
}

// write formats message with specific level and write it
func (writer *baseFileWriter) writef(level Level, format string, args ...interface{}) {
	// 格式化构造message
	// 边解析边输出
	// 使用 % 作占位符

	// 统计日志size
	var size = 0

	defer func() {
		// logrotate
		if writer.sizeRotated || writer.lineRotated {
			writer.logSizeChan <- size
		}
	}()

	if writer.closed {
		return
	}

	size = writer.blog.writef(level, format, args...)
}

// Close close file writer
func (writer *baseFileWriter) Close() {
	if writer.closed {
		return
	}

	writer.blog.Close()
	writer.blog = nil
	writer.closed = true
}

// SetTimeRotated toggle time base logrotate on the fly
func (writer *baseFileWriter) SetTimeRotated(timeRotated bool) {
	writer.timeRotated = timeRotated
}

// RotateSize return size threshold when logrotate
func (writer *baseFileWriter) RotateSize() ByteSize {
	return writer.rotateSize
}

// SetRotateSize set size when logroatate
func (writer *baseFileWriter) SetRotateSize(rotateSize ByteSize) {
	if rotateSize > ByteSize(0) {
		writer.sizeRotated = true
		writer.rotateSize = rotateSize
	} else {
		writer.sizeRotated = false
	}
}

// RotateLine return line threshold when logrotate
func (writer *baseFileWriter) RotateLine() int {
	return writer.rotateLines
}

// SetRotateLines set line number when logrotate
func (writer *baseFileWriter) SetRotateLines(rotateLines int) {
	if rotateLines > 0 {
		writer.lineRotated = true
		writer.rotateLines = rotateLines
	} else {
		writer.lineRotated = false
	}
}

// Colored return whether writer log with color
func (writer *baseFileWriter) Colored() bool {
	return writer.colored
}

// SetColored set logging color
func (writer *baseFileWriter) SetColored(colored bool) {
	if colored == writer.colored {
		return
	}

	writer.colored = colored
	initPrefix(colored)
}

// Level return logging level threshold
func (writer *baseFileWriter) Level() Level {
	return writer.blog.Level()
}

// SetLevel set logging level threshold
func (writer *baseFileWriter) SetLevel(level Level) {
	writer.blog.SetLevel(level)
}

// SetHook do nothing
func (writer *baseFileWriter) SetHook(hook Hook) {
	return
}

// SetHookLevel do nothing
func (writer *baseFileWriter) SetHookLevel(level Level) {
	return
}

// flush flush logs to disk
func (writer *baseFileWriter) flush() {
	writer.blog.flush()
}

// Debug do nothing
func (writer *baseFileWriter) Debug(format string) {
	return
}

// Debugf do nothing
func (writer *baseFileWriter) Debugf(format string, args ...interface{}) {
	return
}

// Trace do nothing
func (writer *baseFileWriter) Trace(format string) {
	return
}

// Tracef do nothing
func (writer *baseFileWriter) Tracef(format string, args ...interface{}) {
	return
}

// Info do nothing
func (writer *baseFileWriter) Info(format string) {
	return
}

// Infof do nothing
func (writer *baseFileWriter) Infof(format string, args ...interface{}) {
	return
}

// Warn do nothing
func (writer *baseFileWriter) Warn(format string) {
	return
}

// Warnf do nothing
func (writer *baseFileWriter) Warnf(format string, args ...interface{}) {
	return
}

// Error do nothing
func (writer *baseFileWriter) Error(format string) {
	return
}

// Errorf do nothing
func (writer *baseFileWriter) Errorf(format string, args ...interface{}) {
	return
}

// Critical do nothing
func (writer *baseFileWriter) Critical(format string) {
	return
}

// Criticalf do nothing
func (writer *baseFileWriter) Criticalf(format string, args ...interface{}) {
	return
}