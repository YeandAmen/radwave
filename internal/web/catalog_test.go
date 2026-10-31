package web_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCatalogueUploadAuthIntegrityAndFilteredRead(t *testing.T) {
	serv, _ := server(t, "127.0.0.1:8080", "catalog-owner")
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, _ := z.Create("jobs.csv")
	f.Write([]byte("source,company,title,url,category,level,remote,posted,first_seen,closed_at,fresher_friendly\ngreenhouse,Example,Security Analyst,https://jobs.example.org/1,security,junior,True,2026-10-28,2026-10-30,,True\ngreenhouse,Example,Closed Analyst,https://jobs.example.org/2,security,junior,True,2026-10-28,2026-10-30,2026-10-30,True\n"))
	z.Close()
	data := archive.Bytes()
	h := sha256.Sum256(data)
	digest := hex.EncodeToString(h[:])
	path := serv.URL + "/api/catalog/chunks?part=0&digest=" + digest
	upload := func(token string) *http.Response {
		req, _ := http.NewRequest("POST", path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+token)
		r, err := serv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Body.Close() })
		return r
	}
	if upload("").StatusCode != 401 {
		t.Fatal("anonymous catalogue upload was allowed")
	}
	if upload("catalog-owner").StatusCode != 200 {
		t.Fatal("owner chunk upload failed")
	}
	bad := request(t, serv.Client(), "POST", serv.URL+"/api/catalog/commit", "catalog-owner", "", map[string]any{"digest": digest, "bytes": len(data) + 1, "version": 1})
	if bad.StatusCode != 400 {
		t.Fatal("incomplete archive was published")
	}
	response := request(t, serv.Client(), "GET", serv.URL+"/api/catalog", "", "", nil)
	var before struct {
		Ready bool `json:"ready"`
	}
	json.NewDecoder(response.Body).Decode(&before)
	if before.Ready {
		t.Fatal("partial catalogue became visible")
	}
	good := request(t, serv.Client(), "POST", serv.URL+"/api/catalog/commit", "catalog-owner", "", map[string]any{"digest": digest, "bytes": len(data), "version": 1})
	if good.StatusCode != 200 {
		t.Fatal("valid catalogue was not committed")
	}
	response = request(t, serv.Client(), "GET", serv.URL+"/api/opportunities?platform=kaggle&field=security&remote=true&fresher=true", "", "", nil)
	var result struct {
		Total int `json:"total"`
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.Total != 1 || result.Items[0].Status != "unknown" {
		t.Fatal("catalogue filtering or closure handling failed")
	}
	response = request(t, serv.Client(), "GET", serv.URL+"/api/opportunities/"+result.Items[0].ID, "", "", nil)
	if response.StatusCode != 200 {
		t.Fatal("catalogue detail lookup failed")
	}
}
