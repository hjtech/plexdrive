package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"time"

	"strings"

	"github.com/claudetech/loggo"
	. "github.com/claudetech/loggo/default"
)

func main() {
	// get the users home dir
	user, err := user.Current()
	if nil != err {
		panic(fmt.Sprintf("Could not read users homedir %v\n", err))
	}

	// parse the command line arguments
	argLogLevel := flag.Int("log-level", 0, "Set the log level (0 = error, 1 = warn, 2 = info, 3 = debug, 4 = trace)")
	argConfigPath := flag.String("config", filepath.Join(user.HomeDir, ".plexdrive"), "The path to the configuration directory")
	argTempPath := flag.String("temp", os.TempDir(), "Path to a temporary directory to store temporary data")
	argChunkSize := flag.Int64("chunk-size", 5*1024*1024, "The size of each chunk that is downloaded (in byte)")
	argRefreshInterval := flag.Duration("refresh-interval", 5*time.Minute, "The number of minutes to wait till checking for changes")
	argClearInterval := flag.Duration("clear-chunk-interval", 1*time.Minute, "The number of minutes to wait till clearing the chunk directory")
	argMountOptions := flag.String("fuse-options", "", "Fuse mount options (e.g. -fuse-options allow_other,...)")
	flag.Parse()

	// check if mountpoint is specified
	argMountPoint := flag.Arg(0)
	if "" == argMountPoint {
		flag.Usage()
		panic(fmt.Errorf("Mountpoint not specified"))
	}

	// parse the mount options
	var mountOptions []string
	if "" != *argMountOptions {
		mountOptions = strings.Split(*argMountOptions, ",")
	}

	// initialize the logger with the specific log level
	var logLevel loggo.Level
	switch *argLogLevel {
	case 0:
		logLevel = loggo.Error
	case 1:
		logLevel = loggo.Warning
	case 2:
		logLevel = loggo.Info
	case 3:
		logLevel = loggo.Debug
	case 4:
		logLevel = loggo.Trace
	default:
		logLevel = loggo.Warning
	}
	Log.SetLevel(logLevel)

	// debug all given parameters
	Log.Debugf("log-level            : %v", logLevel)
	Log.Debugf("config               : %v", *argConfigPath)
	Log.Debugf("temp                 : %v", *argTempPath)
	Log.Debugf("chunk-size           : %v", *argChunkSize)
	Log.Debugf("refresh-interval     : %v", *argRefreshInterval)
	Log.Debugf("clear-chunk-interval : %v", *argClearInterval)
	Log.Debugf("fuse-options         : %v", *argMountOptions)

	// create all directories
	if err := os.MkdirAll(*argConfigPath, 0766); nil != err {
		Log.Errorf("Could not create configuration directory")
		Log.Debugf("%v", err)
		os.Exit(1)
	}
	chunkPath := filepath.Join(*argTempPath, "chunks")
	if err := os.MkdirAll(chunkPath, 0777); nil != err {
		Log.Errorf("Could not create temp chunk directory")
		Log.Debugf("%v", err)
		os.Exit(2)
	}

	// set the global buffer configuration
	SetChunkPath(chunkPath)
	SetChunkSize(*argChunkSize)

	// read the configuration
	configPath := filepath.Join(*argConfigPath, "config.json")
	config, err := ReadConfig(configPath)
	if nil != err {
		config, err = CreateConfig(configPath)
		if nil != err {
			Log.Errorf("Could not read configuration")
			Log.Debugf("%v", err)
			os.Exit(3)
		}
	}

	cache, err := NewCache(filepath.Join(*argConfigPath, "cache"))
	if nil != err {
		Log.Errorf("Could not initialize cache")
		Log.Debugf("%v", err)
		os.Exit(4)
	}
	defer cache.Close()

	drive, err := NewDriveClient(config, cache, *argRefreshInterval)
	if nil != err {
		Log.Errorf("Could not initialize Google Drive Client")
		Log.Debugf("%v", err)
		os.Exit(5)
	}

	go CleanChunkDir(chunkPath, *argClearInterval)
	if err := Mount(drive, argMountPoint, mountOptions); nil != err {
		Log.Debugf("%v", err)
		os.Exit(6)
	}
}
