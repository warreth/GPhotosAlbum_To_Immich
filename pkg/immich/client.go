package immich

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

type ImmichAsset struct {
	Id               string `json:"id"`
	Type             string `json:"type"`
	OriginalFileName string `json:"originalFileName"`
	OriginalMimeType string `json:"originalMimeType"`
	FileCreatedAt    string `json:"fileCreatedAt"`
}

type ImmichAssetResponse struct {
	Assets struct {
		Total int           `json:"total"`
		Count int           `json:"count"`
		Items []ImmichAsset `json:"items"`
	} `json:"assets"`
}

type Album struct {
	AlbumName string `json:"albumName"`
	Id        string `json:"id"`
	OwnerId   string `json:"ownerId"`
	Assets    []struct {
		Id               string `json:"id"`
		OriginalFileName string `json:"originalFileName"`
		OriginalMimeType string `json:"originalMimeType"`
	} `json:"assets"`
}

type Client struct {
	APIURL string
	APIKey string
	Client *http.Client
}

func NewClient(apiURL, apiKey string) *Client {
	// Ensure APIURL doesn't end with slash but allowing it to be handled in getData mainly
	if strings.HasSuffix(apiURL, "/") {
		apiURL = apiURL[:len(apiURL)-1]
	}
	return &Client{
		APIURL: apiURL,
		APIKey: apiKey,
		Client: &http.Client{},
	}
}

func (c *Client) request(method string, path string, payload []byte, contentType string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s", c.APIURL, path)
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Accept", "application/json")
	if contentType != "" {
		req.Header.Add("Content-Type", contentType)
	} else {
		req.Header.Add("Content-Type", "application/json")
	}
	req.Header.Add("x-api-key", c.APIKey)

	res, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode >= 400 {
		return body, fmt.Errorf("API error: %s - %s", res.Status, string(body))
	}

	return body, nil
}

func (c *Client) GetAlbums() ([]Album, error) {
	body, err := c.request("GET", "albums", nil, "")
	if err != nil {
		return nil, err
	}
	var albums []Album
	err = json.Unmarshal(body, &albums)
	return albums, err
}

func (c *Client) CreateAlbum(name string) (*Album, error) {
	payload := map[string]string{"albumName": name}
	jsonPayload, _ := json.Marshal(payload)
	body, err := c.request("POST", "albums", jsonPayload, "")
	if err != nil {
		return nil, err
	}
	var album Album
	err = json.Unmarshal(body, &album)
	return &album, err
}

func (c *Client) AddAssetsToAlbum(albumId string, assetIds []string) error {
	const batchSize = 50 // process in chunks
	for i := 0; i < len(assetIds); i += batchSize {
		end := i + batchSize
		if end > len(assetIds) {
			end = len(assetIds)
		}
		
		chunk := assetIds[i:end]
		payload := map[string]interface{}{"ids": chunk}
		jsonPayload, _ := json.Marshal(payload)
		_, err := c.request("PUT", fmt.Sprintf("albums/%s/assets", albumId), jsonPayload, "")
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) SearchAssets(filename string) ([]ImmichAsset, error) {
	// Simple search by filename
	payload := map[string]string{"originalFileName": filename}
	jsonPayload, _ := json.Marshal(payload)
	body, err := c.request("POST", "search/metadata", jsonPayload, "")
	if err != nil {
		return nil, err
	}
	var resp ImmichAssetResponse
	err = json.Unmarshal(body, &resp)
	return resp.Assets.Items, err
}


func (c *Client) requestWithReader(method string, path string, bodyReader io.Reader, contentType string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s", c.APIURL, path)

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Accept", "application/json")
	if contentType != "" {
		req.Header.Add("Content-Type", contentType)
	} else {
		req.Header.Add("Content-Type", "application/json")
	}
	req.Header.Add("x-api-key", c.APIKey)

	res, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode >= 400 {
		return body, fmt.Errorf("API error: %s - %s", res.Status, string(body))
	}

	return body, nil
}

func (c *Client) UploadAssetStream(reader io.Reader, filename string, size int64, createdAt time.Time, description string) (string, bool, error) {
	pr, pw := io.Pipe()
	multipartWriter := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer multipartWriter.Close()
		
		// Metadata fields
		_ = multipartWriter.WriteField("deviceAssetId", fmt.Sprintf("%s-%d", filename, size))
		_ = multipartWriter.WriteField("deviceId", "immich-sync-go")
		
		creationTime := time.Now()
		if !createdAt.IsZero() {
			creationTime = createdAt
		}
		
		_ = multipartWriter.WriteField("fileCreatedAt", creationTime.Format(time.RFC3339))
		_ = multipartWriter.WriteField("fileModifiedAt", creationTime.Format(time.RFC3339))
		_ = multipartWriter.WriteField("isFavorite", "false")
		if description != "" {
			_ = multipartWriter.WriteField("description", description)
		}

		part, err := multipartWriter.CreateFormFile("assetData", filename)
		if err != nil {
			return
		}
		if _, err := io.Copy(part, reader); err != nil {
			return
		}
	}()

	resp, err := c.requestWithReader("POST", "assets", pr, multipartWriter.FormDataContentType())
	if err != nil {
		return "", false, err
	}
	
	var res map[string]interface{}
	json.Unmarshal(resp, &res)

	isDup := false
	if d, ok := res["duplicate"].(bool); ok && d {
		isDup = true
	}

	if id, ok := res["id"].(string); ok {
		return id, isDup, nil
	}
    
    // Sometimes duplicate comes without id in main body if errored? 
    // Usually immich returns 200/201 with id.
    
	return "", false, nil 
}

func (c *Client) GetUser() (string, string, error) {
    body, err := c.request("GET", "users/me", nil, "")
    if err != nil {
        return "", "", err
    }
    var user struct {
        Id string `json:"id"`
        Name string `json:"name"`
    }
    err = json.Unmarshal(body, &user)
    return user.Id, user.Name, err
}
