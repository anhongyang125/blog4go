// Copyright (c) 2015, huangjunwei <huangjunwei@youmi.net>. All rights reserved.

// Package blog4go provide an efficient and easy-to-use writers library for
// logging into files, console or sockets. Writers suports formatting
// string filtering and calling user defined hook in asynchronous mode.
package blog4go

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const (
	// EOL end of a line
	EOL = '\n'
	// ESCAPE escape character
	ESCAPE = '\\'
	// PLACEHOLDER placeholder
	PLACEHOLDER = '%'
)

var (
	// blog is the singleton instance use for blog.write/writef
	blog Writer

	// global mutex log used for singlton
	singltonLock *sync.Mutex

	// DefaultBufferSize bufio buffer size
	DefaultBufferSize = 4096 // default memory page size
	// ErrInvalidFormat invalid format error
	ErrInvalidFormat = errors.New("Invalid format type")
	// ErrAlreadyInit show that blog is already initialized once
	ErrAlreadyInit = errors.New("blog4go has been already initialized")
)

// Writer interface is a common definition of any writers in this package.
// Any struct implements Writer interface must implement functions below.
// Close is used for close the writer and free any elements if needed.
// write is an internal function that write pure message with specific
// logging level.
// writef is an internal function that formatting message with specific
// logging level. Placeholders in the format string will be replaced with
// args given.
// Both write and writef may have an asynchronous call of user defined
// function before write and writef function end..
type Writer interface {
	// Close do anything end before program end
	Close()

	// Level return logging level threshold
	Level() Level
	// SetLevel set logging level threshold
	SetLevel(level Level)

	// write/writef functions with different levels
	write(level Level, format string)
	writef(level Level, format string, args ...interface{})
	Debug(format string)
	Debugf(format string, args ...interface{})
	Trace(format string)
	Tracef(format string, args ...interface{})
	Info(format string)
	Infof(format string, args ...interface{})
	Warn(format string)
	Warnf(format string, args ...interface{})
	Error(format string)
	Errorf(format string, args ...interface{})
	Critical(format string)
	Criticalf(format string, args ...interface{})

	// flush log to disk
	flush()

	// hook
	SetHook(hook Hook)
	SetHookLevel(level Level)

	// logrotate
	SetTimeRotated(timeRotated bool)
	SetRotateSize(rotateSize ByteSize)
	SetRotateLines(rotateLines int)
	SetRetentions(retentions int64)
	SetColored(colored bool)
}

func init() {
	singltonLock = new(sync.Mutex)
	DefaultBufferSize = os.Getpagesize()
}

// NewWriterFromConfigAsFile initialize a writer according to given config file
// configFile must be the path to the config file
func NewWriterFromConfigAsFile(configFile string) (err error) {
	singltonLock.Lock()
	defer singltonLock.Unlock()
	if nil != blog {
		return
	}

	// read config from file
	config, err := readConfig(configFile)
	if nil != err {
		return
	}

	multiWriter := new(MultiWriter)

	multiWriter.level = DEBUG
	if level := LevelFromString(config.MinLevel); level.valid() {
		multiWriter.level = level
	}

	multiWriter.closed = false
	multiWriter.writers = make(map[Level]Writer)

	for _, filter := range config.Filters {
		var rotate = false
		var isSocket = false

		// get file path
		var filePath string
		if nil != &filter.File && "" != filter.File.Path {
			// single file
			filePath = filter.File.Path
			rotate = false
		} else if nil != &filter.RotateFile && "" != filter.RotateFile.Path {
			// multi files
			filePath = filter.RotateFile.Path
			rotate = true
		} else if nil != &filter.Socket && "" != filter.Socket.Address && "" != filter.Socket.Network {
			isSocket = true
		} else {
			// config error
			return ErrFilePathNotFound
		}

		levels := strings.Split(filter.Levels, ",")
		for _, levelStr := range levels {
			var level Level
			if level = LevelFromString(levelStr); !level.valid() {
				return ErrInvalidLevel
			}

			if isSocket {
				// socket writer
				writer, err := newSocketWriter(filter.Socket.Network, filter.Socket.Address)
				if nil != err {
					return err
				}

				multiWriter.writers[level] = writer
				continue
			}

			// init a base file writer
			writer, err := newBaseFileWriter(filePath, rotate)
			if nil != err {
				return err
			}

			if rotate {
				// set logrotate strategy
				switch filter.RotateFile.Type {
				case TypeTimeBaseRotate:
					writer.SetTimeRotated(true)
				case TypeSizeBaseRotate:
					writer.SetRotateSize(filter.RotateFile.RotateSize)
					writer.SetRotateLines(filter.RotateFile.RotateLines)
					writer.SetRetentions(filter.RotateFile.Retentions)
				default:
					return ErrInvalidRotateType
				}
			}

			// set color
			multiWriter.SetColored(filter.Colored)
			multiWriter.writers[level] = writer
		}
	}

	blog = multiWriter
	return
}

// BLog struct is a threadsafe log writer inherit bufio.Writer
type BLog struct {
	// logging level
	// every message level exceed this level will be written
	level Level

	// input io
	in io.Writer

	// bufio.Writer object of the input io
	writer *bufio.Writer

	// exclusive lock while calling write function of bufio.Writer
	lock *sync.Mutex
}

// NewBLog create a BLog instance and return the pointer of it.
// fileName must be an absolute path to the destination log file
func NewBLog(in io.Writer) (blog *BLog) {
	blog = new(BLog)
	blog.in = in
	blog.level = DEBUG
	blog.lock = new(sync.Mutex)

	blog.writer = bufio.NewWriterSize(in, DefaultBufferSize)
	return
}

// write writes pure message with specific level
func (blog *BLog) write(level Level, format string) int {
	blog.lock.Lock()
	defer blog.lock.Unlock()

	// 统计日志size
	var size = 0

	blog.writer.Write(timeCache.format)
	blog.writer.WriteString(level.prefix())
	blog.writer.WriteString(format)
	blog.writer.WriteByte(EOL)

	size = len(timeCache.format) + len(level.prefix()) + len(format) + 1
	return size
}

// write formats message with specific level and write it
func (blog *BLog) writef(level Level, format string, args ...interface{}) int {
	// 格式化构造message
	// 边解析边输出
	// 使用 % 作占位符
	blog.lock.Lock()
	defer blog.lock.Unlock()

	// 统计日志size
	var size = 0

	// 识别占位符标记
	var tag = false
	var tagPos int
	// 转义字符标记
	var escape = false
	// 在处理的args 下标
	var n int
	// 未输出的，第一个普通字符位置
	var last int
	var s int

	blog.writer.Write(timeCache.format)
	blog.writer.WriteString(level.prefix())

	size += len(timeCache.format) + len(level.prefix())

	for i, v := range format {
		if tag {
			switch v {
			case 'd', 'f', 'v', 'b', 'o', 'x', 'X', 'c', 'p', 't', 's', 'T', 'q', 'U', 'e', 'E', 'g', 'G':
				if escape {
					escape = false
				}

				s, _ = blog.writer.WriteString(fmt.Sprintf(format[tagPos:i+1], args[n]))
				size += s
				n++
				last = i + 1
				tag = false
			//转义符
			case ESCAPE:
				if escape {
					blog.writer.WriteByte(ESCAPE)
					size++
				}
				escape = !escape
			//默认
			default:

			}

		} else {
			// 占位符，百分号
			if PLACEHOLDER == format[i] && !escape {
				tag = true
				tagPos = i
				s, _ = blog.writer.WriteString(format[last:i])
				size += s
				escape = false
			}
		}
	}
	blog.writer.WriteString(format[last:])
	blog.writer.WriteByte(EOL)

	size += len(format[last:]) + 1
	return size
}

// Flush flush buffer to disk
func (blog *BLog) flush() {
	blog.lock.Lock()
	defer blog.lock.Unlock()
	blog.writer.Flush()
}

// Close close file writer
func (blog *BLog) Close() {
	blog.lock.Lock()
	defer blog.lock.Unlock()

	blog.writer.Flush()
	blog.writer = nil
}

// In return the input io.Writer
func (blog *BLog) In() io.Writer {
	return blog.in
}

// Level return logging level threshold
func (blog *BLog) Level() Level {
	return blog.level
}

// SetLevel set logging level threshold
func (blog *BLog) SetLevel(level Level) *BLog {
	blog.level = level
	return blog
}

// resetFile resets file descriptor of the writer with specific file name
func (blog *BLog) resetFile(in io.Writer) (err error) {
	blog.lock.Lock()
	defer blog.lock.Unlock()
	blog.writer.Flush()

	blog.in = in
	blog.writer.Reset(in)

	return
}

// SetHook set hook for logging action
func SetHook(hook Hook) {
	blog.SetHook(hook)
}

// SetHookLevel set when hook will be called
func SetHookLevel(level Level) {
	blog.SetHookLevel(level)
}

// SetColored set logging color
func SetColored(colored bool) {
	blog.SetColored(colored)
}

// SetTimeRotated toggle time base logrotate on the fly
func SetTimeRotated(timeRotated bool) {
	blog.SetTimeRotated(timeRotated)
}

// SetRetentions set how many logs will keep after logrotate
func SetRetentions(retentions int64) {
	blog.SetRetentions(retentions)
}

// SetRotateSize set size when logroatate
func SetRotateSize(rotateSize ByteSize) {
	blog.SetRotateSize(rotateSize)
}

// SetRotateLines set line number when logrotate
func SetRotateLines(rotateLines int) {
	blog.SetRotateLines(rotateLines)
}

// Flush flush logs to disk
func Flush() {
	blog.flush()
}

// Debug static function for Debug
func Debug(format string) {
	blog.Debug(format)
}

// Debugf static function for Debugf
func Debugf(format string, args ...interface{}) {
	blog.Debugf(format, args...)
}

// Trace static function for Trace
func Trace(format string) {
	blog.Trace(format)
}

// Tracef static function for Tracef
func Tracef(format string, args ...interface{}) {
	blog.Tracef(format, args...)
}

// Info static function for Info
func Info(format string) {
	blog.Info(format)
}

// Infof static function for Infof
func Infof(format string, args ...interface{}) {
	blog.Infof(format, args...)
}

// Warn static function for Warn
func Warn(format string) {
	blog.Warn(format)
}

// Warnf static function for Warnf
func Warnf(format string, args ...interface{}) {
	blog.Warnf(format, args...)
}

// Error static function for Error
func Error(format string) {
	blog.Error(format)
}

// Errorf static function for Errorf
func Errorf(format string, args ...interface{}) {
	blog.Errorf(format, args...)
}

// Critical static function for Critical
func Critical(format string) {
	blog.Critical(format)
}

// Criticalf static function for Criticalf
func Criticalf(format string, args ...interface{}) {
	blog.Criticalf(format, args...)
}

// Close close the logger
func Close() {
	singltonLock.Lock()
	defer singltonLock.Unlock()

	blog.Close()
	blog = nil
}
