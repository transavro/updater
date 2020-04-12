package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Updater struct {
	Action          string `json:"action"`
	VersionCode     int    `json:"versionCode"`
	PackageName     string `json:"packageName"`
	BuildDate       string `json:"buildDate"`
	Md5             string `json:"md5"`
	Size            string `json:"size"`
	DownloadURL     string `json:"downloadUrl"`
	UpdateChangeLog string `json:"updateChangeLog"`
	IsForced        bool   `json:"isForced"`
}

func GetBytes(key interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(key)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func main() {
	reqBody, err := makingRequestBody()
	if err != nil {
		log.Fatal(err)
	}

	jsonString, err := json.Marshal(reqBody)

	req, err := http.NewRequest("GET", "http://192.168.1.9:9876/update.json", bytes.NewBuffer(jsonString))
	if err != nil {
		log.Fatalln(err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Version", "1.0.0")

	// Send request
	// Set client timeout
	client := &http.Client{Timeout: time.Second * 10}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal("Error reading response. ", err)
	}

	defer resp.Body.Close()

	log.Println("response code ====>", resp.StatusCode)

	var result []*Updater
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		log.Fatal(err)
	}

	for _, updater := range result {
		err = handlingUpdate(updater)
		if err != nil {
			log.Fatal(err)
		}
	}
	log.Println("Completed.")
}

func downloadNInstall(update *Updater) (err error) {
	// Downloading a file.
	updateFile, err := DownloadFile(update.DownloadURL)
	if err != nil {
		return err
	}
	//installing a file
	err = updateProcess(updateFile, update, "sh", "-c", "pm install "+updateFile.Name())
	if err == nil {
		// check if the app is installed or not
		result, err := execute("sh", "-c", "pm list packages  "+update.PackageName)
		if err != nil {
			return err
		}
		if result == "" {
			return errors.New("App not installed please check the command")
		}
		log.Println(update.PackageName, "  installed successfully.")
		return err
	}
	return err
}

func uninstallApp(packageName string) error {
	// uninstalling the app will give two result first will give "success" or a big stack trace.
	cmdOut, err := exec.Command("sh", "-c", fmt.Sprintf("pm uninstall  %s", packageName)).Output()
	if err != nil {
		return err
	}
	if strings.Compare("Success", string(cmdOut)) == 1 {
		return errors.New(fmt.Sprintf("Unable to uninstall the app %s", packageName))
	}
	log.Println(packageName, "  uninstalled successfully.")
	return nil
}

func compareAppVersion(serverAppVersion int, packageName string) (isServerVersionHigher bool, err error) {
	// if app is already installed.. compare the version of the app.
	log.Printf("app already installed %s", packageName)
	cmdOut, err := exec.Command("sh", "-c", fmt.Sprintf("dumpsys package %s | grep versionCode", packageName)).Output()
	if err != nil {
		return false, err
	}
	var deviceAppVersion int
	for _, s := range strings.Split(string(cmdOut), " ") {
		if s != "" && strings.Contains(s, "versionCode") {
			deviceAppVersion, _ = strconv.Atoi(strings.TrimSpace(strings.Replace(s, "versionCode=", "", -1)))
			break
		}
	}
	if serverAppVersion > deviceAppVersion {
		return true, nil
	} else {
		return false, nil
	}
}

func isAppInstalled(packageName string) (bool, error) {
	// check if the app is installed or not
	if result, err := execute("sh", "-c", "pm list packages  "+packageName); err != nil {
		return false, err
	} else if result == "" {
		return true, nil
	} else {
		return false, nil
	}
}

func comapareOTAVersion(buildDate string, isFOTA bool) (isOTAVersionHigher bool, err error) {
	// compare the fota version
	var data string
	if isFOTA {
		data, err = getProp([]string{"ro.cvte.ota.version", "ro.build.date.utc"})
		if err != nil {
			return false, err
		}
	} else {
		data, err = getProp([]string{"ro.cloudwalker.cota.version"})
		if err != nil {
			return false, err
		}
	}

	localVersion, err := strconv.Atoi(strings.TrimSpace(strings.Replace(strings.Replace(data, "\n", "", -1), "_", "", -1)))
	if err != nil {
		return false, err
	}
	serverVersion, err := strconv.Atoi(strings.Replace(buildDate, "_", "", -1))
	if err != nil {
		return false, err
	}
	if serverVersion > localVersion {
		return true, nil
	} else {
		return false, nil
	}
}

func handlingUpdate(update *Updater) error {

	var (
		updateFile *os.File
	)

	switch update.Action {
	case "upgrade":
	case "install":
		{
			// check if the app is installed or not
			if result, err := isAppInstalled(update.PackageName); err != nil {
				return err
			} else if result {
				// comparing app versions
				if isHigher, err := compareAppVersion(update.VersionCode, update.PackageName); err != nil {
					return err
				} else if isHigher {
					return downloadNInstall(update)
				}
			} else {
				return downloadNInstall(update)
			}
		}
	case "uninstall":
		{
			return uninstallApp(update.PackageName)
		}
	case "downgrade":
		{
			// check if the app is installed or not
			if result, err := isAppInstalled(update.PackageName); err != nil {
				return err
			} else if result {
				// comparing app versions
				if isHigher, err := compareAppVersion(update.VersionCode, update.PackageName); err != nil {
					return err
				} else if !isHigher {
					// Bingo got the lower version, so uninstall the higherVersion and download n install the lower version
					if err = uninstallApp(update.PackageName); err != nil {
						return err
					}
					return downloadNInstall(update)
				}
			} else {
				return downloadNInstall(update)
			}
		}
	case "fota":
		{
			// compare the fota version
			if result, err := comapareOTAVersion(update.BuildDate, true); err != nil {
				return err
			} else if result {
				// Downloading a zip file.
				if updateFile, err = DownloadFile(update.DownloadURL); err != nil {
					return err
				} else {
					return updateProcess(updateFile, update, "reboot", "recovery")
				}
			} else {
				return nil
			}
		}
	case "cota":
		{
			// compare the cota version
			if result, err := comapareOTAVersion(update.BuildDate, false); err != nil {
				return err
			} else if result {
				// Downloading a zip file.
				if updateFile, err = DownloadFile(update.DownloadURL); err != nil {
					return err
				} else {
					return updateProcess(updateFile, update, "reboot", "recovery")
				}
			} else {
				return nil
			}
		}
	}
	return nil
}

func updateProcess(updateFile *os.File, updater *Updater, command string, args ...string) error {

	md5Result, err := execute("md5sum", updateFile.Name())
	if err != nil {
		return err
	}
	runes := []rune(md5Result)
	md5Result = string(runes[0:32])

	if md5Result == updater.Md5 {
		log.Println("***MD5 matched***")
		if strings.Contains(filepath.Ext(updateFile.Name()), "zip") {
			log.Printf("In Zip commands %s %s", command, args)
			// command file creating
			commandFile, err := os.Create("/cache/recovery/command")
			if err != nil {
				return err
			}
			// writing the string in command-
			_, err = commandFile.WriteString(fmt.Sprintf("--update_package=%s", updateFile.Name()))
			defer commandFile.Close()
			if err != nil {
				return err
			}
			log.Println(command, args)
			_, err = execute(command, args...)
			if err != nil {
				return err
			}
		} else {
			log.Printf("In apk commands %s %s", command, args)
			_, err = execute(command, args...)
			if err != nil {
				return err
			}
		}
	} else {
		return errors.New("MD5 not matched.")
	}
	return nil
}

func execute(command string, arg ...string) (string, error) {
	// let's try the pwd command here
	out, err := exec.Command(command, arg...).Output()
	if err != nil {
		return "", err
	}
	return string(out[:]), nil
}

// DownloadFile will download a url to a local file. It's efficient because it will
// write as it downloads and not load the whole file into memory.
func DownloadFile(targetUrl string) (*os.File, error) {
	myUrl, err := url.Parse(targetUrl)
	if err != nil {
		log.Fatal(err)
	}
	lastSeg := path.Base(myUrl.Path)
	var fileLocation string

	if strings.Contains(lastSeg, "apk") {
		fileLocation = "/sdcard/" + lastSeg
	} else {
		fileLocation = "/cache/" + lastSeg
	}
	// Get the data
	resp, err := http.Get(targetUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Create the file
	out, err := os.Create(fileLocation)
	if err != nil {
		return nil, err
	}

	defer out.Close()
	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	return out, err
}

func makingRequestBody() (map[string]interface{}, error) {
	output := make(map[string]interface{})
	//getting adroid api level
	data, err := execute("getprop", "ro.build.version.sdk")
	if err != nil {
		return nil, err
	}
	if data != "" {
		output["android"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))
	}
	// getting brand
	brandInfo, err := getBrand()
	if err != nil {
		return nil, err
	}
	output["brand"] = brandInfo[0]
	output["brandUiVersion"] = brandInfo[1]

	// getting cota
	data, err = getProp([]string{"ro.cloudwalker.cota.version"})
	if err != nil {
		return nil, err
	}
	output["cota"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	// making emac
	emacBytes, err := ioutil.ReadFile("/sys/class/net/eth0/address")
	if err != nil {
		return nil, err
	}
	output["emac"] = strings.TrimSpace(string(emacBytes))

	// making fota
	data, err = getProp([]string{"ro.cvte.ota.version", "ro.build.date.utc"})
	if err != nil {
		return nil, err
	}
	output["fota"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	// making board
	data, err = getProp([]string{"ro.cvte.boardname", "ro.board.platform"})
	if err != nil {
		return nil, err
	}
	output["mboard"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	// making model
	data, err = getProp([]string{"ro.product.model"})
	if err != nil {
		return nil, err
	}
	output["model"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	// launcher packageName
	data, err = getLauncherPackage()
	if err != nil {
		return nil, err
	}
	output["lpackageName"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	//making panel
	data, err = getProp([]string{"ro.cvte.panelname"})
	if err != nil {
		return nil, err
	}
	output["panel"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	//making serial
	data, err = getProp([]string{"ro.boot.serialno"})
	if err != nil {
		return nil, err
	}
	output["serial"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	//making emac
	data, err = readFromFile("/sys/class/net/eth0/address")
	if err != nil {
		return nil, err
	}
	output["emac"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	//making wmac
	data, err = readFromFile("/sys/class/net/wlan0/address")
	if err != nil {
		return nil, err
	}
	output["wmac"] = strings.TrimSpace(strings.Replace(data, "\n", "", -1))

	// making applist
	applist, err := getInstalledAppList()
	if err != nil {
		return nil, err
	}
	output["apps"] = applist

	// making launcher versionName
	cmdOut, err := exec.Command("sh", "-c", "dumpsys package tv.cloudwalker.cwnxt.launcher.com | grep versionName").Output()
	if err != nil {
		return nil, err
	}
	output["lversionName"] = strings.TrimSpace(strings.Replace(string(cmdOut), "versionName=", "", -1))

	// making launcher versionCode
	cmdOut, err = exec.Command("sh", "-c", "dumpsys package tv.cloudwalker.cwnxt.launcher.com | grep versionCode").Output()
	if err != nil {
		return nil, err
	}
	for _, s := range strings.Split(string(cmdOut), " ") {
		if s != "" && strings.Contains(s, "versionCode") {
			output["lversionCode"] = strings.TrimSpace(strings.Replace(s, "versionCode=", "", -1))
			break
		}
	}
	return output, nil
}

func getInstalledAppList() (map[string]interface{}, error) {
	appMap := make(map[string]interface{})
	output, err := exec.Command("sh", "-c", "pm list packages").Output()
	if err != nil {
		log.Fatal(err)
	}
	appList := strings.TrimSpace(string(output))

	for _, appPackage := range strings.Split(appList, "package:") {
		if len(appPackage) == 0 {
			continue
		}
		output, _ := exec.Command("sh", "-c", "dumpsys package "+strings.TrimSpace(appPackage)+"| grep versionName").Output()
		appMap[strings.TrimSpace(appPackage)] = strings.TrimSpace(strings.Replace(string(output), "versionName=", "", -1))
	}
	return appMap, nil
}

func getLauncherPackage() (string, error) {
	result, err := execute("sh", "-c", "pm list packages tv.cloudwalker.cwnxt.launcher.com")
	if err != nil {
		return "", err
	}

	// TODO get the packageNamne of airstream
	//if result == "" {
	//	result, err = execute("sh", "-c", "pm list packages tv.cloudwalker.cwnxt.launcher.com")
	//	if err != nil {
	//		return "",  err
	//	}
	//}

	result = strings.Replace(result, "package:", "", -1)
	return result, nil
}

func getProp(keys []string) (string, error) {
	data, err := execute("getprop", keys[0])
	if err != nil {
		return "", err
	}
	if data == "" && len(keys) > 1 {
		data, err = execute("getprop", keys[1])
		if err != nil {
			return "", err
		}
	}
	return data, err
}

func readFromFile(fileLocation string) (string, error) {
	data, err := ioutil.ReadFile(fileLocation)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func getBrand() ([]string, error) {
	// check if the uiconfig file exist or not
	uiconfigPath := ""
	var uiconfig map[string]interface{}
	if _, err := os.Stat("/system/etc/cloudwalker_assets/launcherUiConfig.json"); err == nil {
		uiconfigPath = "/system/etc/cloudwalker_assets/launcherUiConfig.json"
	} else if _, err := os.Stat("/vendor/etc/cloudwalker_assets/launcherUiConfig.json"); err == nil {
		uiconfigPath = "/vendor/etc/cloudwalker_assets/launcherUiConfig.json"
	}

	if uiconfigPath == "" {
		result, err := execute("getprop", "ro.product.brand")
		if err != nil {
			return nil, err
		}
		return []string{result}, err
	}

	data, err := ioutil.ReadFile(uiconfigPath)
	if err != nil {
		return nil, err
	}

	if err = json.Unmarshal(data, &uiconfig); err != nil {
		return nil, err
	}
	return []string{fmt.Sprint(uiconfig["brand"]), fmt.Sprint(uiconfig["uiVersion"])}, nil
}
