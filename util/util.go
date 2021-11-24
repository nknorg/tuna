package util

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"
)

func ReadJSON(fileName string, value interface{}) error {
	file, err := ioutil.ReadFile(fileName)
	if err != nil {
		return fmt.Errorf("read file error: %v", err)
	}

	err = json.Unmarshal(file, value)
	if err != nil {
		return fmt.Errorf("parse json error: %v", err)
	}

	return nil
}

func WriteJSON(path string, data interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err != nil {
		return err
	}
	return nil
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsExist(err) {
			return true
		}
		return false
	}
	return true
}

func DownloadJsonFile(ctx context.Context, url, filename string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	client := http.Client{
		Timeout: 60 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if !json.Valid(b) {
		return errors.New("invalid json")
	}
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	if err != nil {
		os.Remove(filename)
		return err
	}
	return nil
}

func DeepCopyMap(value map[string]interface{}) map[string]interface{} {
	newMap := make(map[string]interface{})
	for k, v := range value {
		newMap[k] = v
	}

	return newMap
}

func CopyFile(src, dst string) error {
	input, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(dst, input, 0644)
	if err != nil {
		return err
	}
	return nil
}
