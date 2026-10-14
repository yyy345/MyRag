package ports

import (
	"strings"
	"testing"
)

const sampleResult = `{"schema_version":1,"task_id":"11111111-1111-4111-8111-111111111111","entry_id":"22222222-2222-4222-8222-222222222222","entry_version":1,"trace_id":"test","status":"COMPLETED","chunk_count":0,"error_code":null}`

func TestResultContract(t *testing.T) {
	if _, err := DecodeIndexResult([]byte(sampleResult)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(sampleResult, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(sampleResult, `"chunk_count":0,`, "", 1),
		strings.Replace(sampleResult, `"chunk_count":0`, `"chunk_count":null`, 1),
		strings.Replace(sampleResult, `"chunk_count":0`, `"chunk_count":-1`, 1),
		strings.Replace(sampleResult, `"chunk_count":0`, `"chunk_count":0,"chunk_count":1`, 1),
		strings.Replace(sampleResult, `"status":"COMPLETED"`, `"status":"FAILED"`, 1),
		strings.Replace(sampleResult, `"error_code":null`, `"error_code":"secret"`, 1),
		strings.Replace(sampleResult, `"entry_version":1`, `"entry_version":0`, 1),
		strings.Replace(sampleResult, `"trace_id":"test"`, `"trace_id":""`, 1),
		sampleResult + ` {}`, `[]`, strings.Repeat(" ", 65537),
		strings.Replace(sampleResult, `"error_code":null`, `"extra":true,"error_code":null`, 1),
	} {
		if _, err := DecodeIndexResult([]byte(bad)); err == nil {
			t.Fatalf("invalid result accepted: %.100s", bad)
		}
	}
}
