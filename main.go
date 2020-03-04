package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/howeyc/fsnotify"
)

var watchExtsStatic = []string{".html", ".tpl", ".js", ".css"}
var watchExts = []string{".go"}
var eventTime = make(map[string]int64)
var scheduleTime time.Time
var state sync.Mutex

// run子命令
var cmd *exec.Cmd

var mainFilePath string
var projectPath string
var runargs string

func init() {
	flag.StringVar(&mainFilePath, "main_file_path", "", "main文件地址")
	flag.StringVar(&projectPath, "path", "", "项目目录, 因为需要进入目录进行build操作")
	flag.StringVar(&runargs, "runargs", "", "运行参数 比如 -conf xxx/app.toml")
}

// 开始监听
func newWatcher(paths []string) (watcher *fsnotify.Watcher, err error) {
	// 开启文件监听对象
	watcher, err = fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	// 监听处理事件
	go func() {
		for {
			select {
			case e := <-watcher.Event:
				isBuild := true
				// 是否是静态文件
				if ifStaticFile(e.Name) {
					continue
				}
				// 是否是go结尾后缀文件
				if !shouldWatchFileWithExtension(e.Name) {
					continue
				}
				// 获取修改事件
				mt := GetFileModTime(e.Name)
				fmt.Println(mt)
				if t := eventTime[e.Name]; mt == t {
					log.Println("自动构建时间重复，跳过.....")
					isBuild = false
				}
				eventTime[e.Name] = mt

				// 是否开始build
				if isBuild {
					log.Println("重新build...")

					go func() {
						// 休眠1秒，再进行操作
						scheduleTime = time.Now().Add(1 * time.Second)
						time.Sleep(scheduleTime.Sub(time.Now()))
						// 开始自动build
						AutoBuildAndRun()
					}()
				}

				log.Println("event:", e)
			case e := <-watcher.Error:
				log.Println("error:", e)
			}
		}
	}()

	log.Println("初始化watcher...")
	for _, path := range paths {
		log.Printf("Watching: "+"%s", path)
		// 监听所有子目录
		err = watcher.Watch(path)
		if err != nil {
			log.Printf("Failed to watch directory: %s", err)
		}
	}

	return
}

// 校验是否是静态文件
func ifStaticFile(filename string) bool {
	for _, s := range watchExtsStatic {
		if strings.HasSuffix(filename, s) {
			return true
		}
	}
	return false
}

// 监听特定结尾的文件
func shouldWatchFileWithExtension(name string) bool {
	for _, s := range watchExts {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// 获取文件的修改时间 int时间戳
func GetFileModTime(path string) int64 {
	path = strings.Replace(path, "\\", "/", -1)
	f, err := os.Open(path)
	if err != nil {

		log.Printf("Failed to open file on '%s': %s", path, err)
		return time.Now().Unix()
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		log.Printf("Failed to get file stats: %s", err)
		return time.Now().Unix()
	}

	return fi.ModTime().Unix()
}

// 自动构建
func AutoBuildAndRun() {
	state.Lock()
	defer state.Unlock()

	os.Chdir(projectPath)

	cmdName := "go"
	var (
		err    error
		stderr bytes.Buffer
	)
	appName := "go-app-debug"
	if runtime.GOOS == "windows" {
		appName += ".exe"
	}

	args := []string{"build"}
	args = append(args, "-o", appName)

	args = append(args, mainFilePath)

	bcmd := exec.Command(cmdName, args...)
	bcmd.Env = os.Environ()
	bcmd.Stderr = &stderr
	err = bcmd.Run()
	if err != nil {
		log.Printf("构建应用失败: %s", stderr.String())
		return
	}

	log.Println("构建成功!")
	Restart(appName)
}

// 重启
func Restart(appname string) {
	log.Println("杀死正在运行的进程")
	Kill()
	go Start(appname)
}

// 杀掉现有进程
func Kill() {
	defer func() {
		if e := recover(); e != nil {
			log.Printf("Kill recover: %s", e)
		}
	}()
	if cmd != nil && cmd.Process != nil {
		err := cmd.Process.Kill()
		if err != nil {
			log.Printf("Error while killing cmd process: %s", err)
		}
	}
}

// 启动run
func Start(appname string) {
	log.Printf("启动run '%s'...", appname)
	if !strings.Contains(appname, "./") {
		appname = "./" + appname
	}

	cmd = exec.Command(appname)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if runargs != "" {
		r := regexp.MustCompile("'.+'|\".+\"|\\S+")
		m := r.FindAllString(runargs, -1)
		cmd.Args = append([]string{appname}, m...)
	} else {
		cmd.Args = append([]string{appname}, []string{}...)
	}
	cmd.Env = os.Environ()

	go cmd.Run()
}

// 获取当前文件夹下所有需要监听的文件， 否则监听不到
func readAppDirectories(directory string, paths *[]string) {
	fileInfos, err := ioutil.ReadDir(directory)
	if err != nil {
		return
	}

	useDirectory := false
	for _, fileInfo := range fileInfos {

		if fileInfo.IsDir() && fileInfo.Name()[0] != '.' {
			readAppDirectories(directory+"/"+fileInfo.Name(), paths)
			continue
		}

		if useDirectory {
			continue
		}

		if path.Ext(fileInfo.Name()) == ".go" {
			*paths = append(*paths, directory)
			useDirectory = true
		}
	}
}

func IsDirectory(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fileInfo.IsDir(), err
}

func initArgs() {
	// 判断目录是否存在
	var err error
	var isDir bool
	isDir, err = IsDirectory(projectPath)
	if err != nil || isDir == false {
		projectPath, err = os.Getwd()

		if err != nil {
			log.Fatal("获取项目目录错误", err)
		}
		if len(mainFilePath) <= 0 {
			mainFilePath = projectPath + "/main.go"
		}
	}

	if len(runargs) <= 0 {
		runargs = fmt.Sprintf("-conf %s/app.toml", projectPath)
	}
	//fmt.Println("===================")
	//fmt.Println(projectPath)
	//fmt.Println(mainFilePath)
	//fmt.Println(len(mainFilePath))
	//fmt.Println(runargs)
}

func main() {
	// 解析命令行
	flag.Parse()

	done := make(chan bool)
	var paths []string

	initArgs()
	//遍历获取所有子目录, 主要目的是用于监听
	readAppDirectories(projectPath, &paths)

	watcher, err := newWatcher(paths)
	if err != nil {
		log.Println("newWatcher报错:", err)
	}
	log.Println("首次AutoBuildAndRun...")
	// 首次构建
	AutoBuildAndRun()

	// 是否退出
	<-done
	watcher.Close()
}
