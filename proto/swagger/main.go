// simple tool to merge different swagger/openapi definition into a single file
// Supports both Swagger 2.0 and OpenAPI 3.0 formats
package main

import (
	"encoding/json"
	"log"
	"os"
	"path"
	"strings"
)

const apiVersion = "1.0.0"
const apiTitle = "grbpwr-products-manager REST API"
const apiDescription = "REST API for grbpwr products manager service"

// Swagger 2.0 model
type swagger2Model struct {
	Swagger     string                 `json:"swagger"`
	BasePath    string                 `json:"basePath"`
	Info        swaggerInfo            `json:"info"`
	Schemes     []string               `json:"schemes"`
	Consumes    []string               `json:"consumes"`
	Produces    []string               `json:"produces"`
	Paths       map[string]interface{} `json:"paths"`
	Definitions map[string]interface{} `json:"definitions"`
}

// OpenAPI 3.0 model
type openAPI3Model struct {
	OpenAPI    string                 `json:"openapi"`
	Info       swaggerInfo            `json:"info"`
	Servers    []interface{}          `json:"servers,omitempty"`
	Paths      map[string]interface{} `json:"paths"`
	Components map[string]interface{} `json:"components,omitempty"`
}

type swaggerInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: go run main.go inputPath")
	}

	// Detect format by reading first file
	fileInfos, err := os.ReadDir(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	var isOpenAPI3 bool
	var firstFile string
	for _, fileInfo := range fileInfos {
		if strings.HasSuffix(fileInfo.Name(), ".swagger.json") || strings.HasSuffix(fileInfo.Name(), ".json") {
			firstFile = path.Join(os.Args[1], fileInfo.Name())
			break
		}
	}

	if firstFile != "" {
		b, err := os.ReadFile(firstFile)
		if err == nil {
			var testFormat map[string]interface{}
			if err := json.Unmarshal(b, &testFormat); err == nil {
				if _, ok := testFormat["openapi"]; ok {
					isOpenAPI3 = true
				}
			}
		}
	}

	if isOpenAPI3 {
		mergeOpenAPI3(os.Args[1])
	} else {
		mergeSwagger2(os.Args[1])
	}
}

func mergeSwagger2(inputPath string) {
	swagger := swagger2Model{
		Swagger:     "2.0",
		Consumes:    []string{"application/json"},
		Produces:    []string{"application/json"},
		Paths:       make(map[string]interface{}),
		Definitions: make(map[string]interface{}),
	}
	swagger.Info.Title = apiTitle
	swagger.Info.Version = apiVersion
	swagger.Info.Description = apiDescription

	fileInfos, err := os.ReadDir(inputPath)
	if err != nil {
		log.Fatal(err)
	}

	for _, fileInfo := range fileInfos {
		if !strings.HasSuffix(fileInfo.Name(), ".swagger.json") && !strings.HasSuffix(fileInfo.Name(), ".json") {
			continue
		}

		b, err := os.ReadFile(path.Join(inputPath, fileInfo.Name()))
		if err != nil {
			log.Fatal(err)
		}

		// replace "title" by "description" for fields
		b = []byte(strings.Replace(string(b), `"title"`, `"description"`, -1))

		var m swagger2Model
		err = json.Unmarshal(b, &m)
		if err != nil {
			// Skip if not Swagger 2.0 format
			continue
		}

		for k, v := range m.Paths {
			swagger.Paths[k] = v
		}
		for k, v := range m.Definitions {
			swagger.Definitions[k] = v
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	err = enc.Encode(swagger)
	if err != nil {
		log.Fatal(err)
	}
}

func mergeOpenAPI3(inputPath string) {
	openapi := openAPI3Model{
		OpenAPI: "3.0.3",
		Info: swaggerInfo{
			Title:       apiTitle,
			Version:     apiVersion,
			Description: apiDescription,
		},
		Servers: []interface{}{
			map[string]string{
				"url":         "/",
				"description": "Default server",
			},
		},
		Paths:      make(map[string]interface{}),
		Components: make(map[string]interface{}),
	}

	fileInfos, err := os.ReadDir(inputPath)
	if err != nil {
		log.Fatal(err)
	}

	schemas := make(map[string]interface{})
	securitySchemes := make(map[string]interface{})

	for _, fileInfo := range fileInfos {
		if !strings.HasSuffix(fileInfo.Name(), ".swagger.json") && !strings.HasSuffix(fileInfo.Name(), ".json") {
			continue
		}

		b, err := os.ReadFile(path.Join(inputPath, fileInfo.Name()))
		if err != nil {
			log.Fatal(err)
		}

		var m openAPI3Model
		err = json.Unmarshal(b, &m)
		if err != nil {
			// Try to convert Swagger 2.0 to OpenAPI 3.0
			var s2 swagger2Model
			if err2 := json.Unmarshal(b, &s2); err2 == nil {
				// Convert Swagger 2.0 to OpenAPI 3.0
				for k, v := range s2.Paths {
					openapi.Paths[k] = v
				}
				if s2.Definitions != nil {
					for k, v := range s2.Definitions {
						schemas[k] = v
					}
				}
				continue
			}
			// Skip if neither format
			continue
		}

		// Merge OpenAPI 3.0
		for k, v := range m.Paths {
			openapi.Paths[k] = v
		}
		if m.Components != nil {
			if schemasMap, ok := m.Components["schemas"].(map[string]interface{}); ok {
				for k, v := range schemasMap {
					schemas[k] = v
				}
			}
			if secMap, ok := m.Components["securitySchemes"].(map[string]interface{}); ok {
				for k, v := range secMap {
					securitySchemes[k] = v
				}
			}
		}
	}

	if len(schemas) > 0 {
		openapi.Components["schemas"] = schemas
	}
	if len(securitySchemes) > 0 {
		openapi.Components["securitySchemes"] = securitySchemes
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	err = enc.Encode(openapi)
	if err != nil {
		log.Fatal(err)
	}
}
