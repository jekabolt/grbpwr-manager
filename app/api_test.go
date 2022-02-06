package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/auth"
	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/matryer/is"
)

var (
	S3AccessKey       = "xxx"
	S3SecretAccessKey = "xxx"
	S3Endpoint        = "fra1.digitaloceanspaces.com"
	bucketName        = "grbpwr"
	bucketLocation    = "fra-1"
	imageStorePrefix  = "grbpwr-com"

	BuntDBProductsPath    = "../bunt/products.db"
	BuntDBArticlesPath    = "../bunt/articles.db"
	BuntDBSalesPath       = "../bunt/sales.db"
	BuntDBSubscribersPath = "../bunt/subscribers.db"
	BuntDBHeroPath        = "../bunt/hero.db"

	serverPort = "8080"

	jwtSecret   = "jwtSecret"
	adminSecret = "adminSecret"
	hosts       = []string{"*"}
)

func bucketFromConst() *bucket.Bucket {
	return &bucket.Bucket{
		S3AccessKey:       S3AccessKey,
		S3SecretAccessKey: S3SecretAccessKey,
		S3Endpoint:        S3Endpoint,
		S3BucketName:      bucketName,
		S3BucketLocation:  bucketLocation,
		ImageStorePrefix:  imageStorePrefix,
	}
}

func buntFromConst() *store.BuntDB {
	return &store.BuntDB{
		BuntDBProductsPath:    BuntDBProductsPath,
		BuntDBArticlesPath:    BuntDBArticlesPath,
		BuntDBSalesPath:       BuntDBSalesPath,
		BuntDBSubscribersPath: BuntDBSubscribersPath,
		BuntDBHeroPath:        BuntDBHeroPath,
	}
}

func InitServerFromConst() *Server {
	b := bucketFromConst()
	db := buntFromConst()
	db.InitDB()
	ac := &auth.Config{
		AdminSecret: adminSecret,
		JWTSecret:   jwtSecret,
	}
	return &Server{
		DB:     db,
		Bucket: b,
		Auth:   ac.New(),
		Config: &config.Config{
			Port:  serverPort,
			Hosts: hosts,
			Auth:  ac,
			Debug: true,
		},
	}
}

func testRequest(t *testing.T, ts *httptest.Server, method, path string, body io.Reader, response interface{}, at string) (*http.Response, interface{}) {

	is := is.New(t)

	req, err := http.NewRequest(method, ts.URL+path, body)
	is.NoErr(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", at))

	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)

	defer resp.Body.Close()

	err = json.NewDecoder(resp.Body).Decode(response)
	is.NoErr(err)

	return resp, response
}

func (s *Server) getAuthRequestPw() *bytes.Reader {
	a := auth.AuthRequest{
		Password: s.Auth.AdminSecret,
	}
	aBytes, _ := json.Marshal(a)
	return bytes.NewReader(aBytes)
}

func (s *Server) getAuthRequestWrongPw() *bytes.Reader {
	a := auth.AuthRequest{
		Password: s.Auth.AdminSecret + "wrong",
	}
	aBytes, _ := json.Marshal(a)
	return bytes.NewReader(aBytes)
}

func (s *Server) getAuthRequestRefresh(rt string) *bytes.Reader {
	a := auth.AuthRequest{
		RefreshToken: rt,
	}
	aBytes, _ := json.Marshal(a)
	return bytes.NewReader(aBytes)
}

func (s *Server) getAuthRequestWrongRefresh(rt string) *bytes.Reader {
	a := auth.AuthRequest{
		RefreshToken: rt + "wrong",
	}
	aBytes, _ := json.Marshal(a)
	return bytes.NewReader(aBytes)

}

func TestAuthTokenByPasswordAndRefresh(t *testing.T) {
	is := is.New(t)

	s := InitServerFromConst()

	ts := httptest.NewServer(s.Router())
	defer ts.Close()

	// auth w password
	authResp := &AuthResponse{}
	res, ar := testRequest(t, ts, http.MethodPost, "/auth", s.getAuthRequestPw(), authResp, "")
	authResp = ar.(*AuthResponse)
	is.Equal(res.StatusCode, http.StatusOK)
	// t.Logf("%+v", authResp)

	// auth w refresh
	res, ar = testRequest(t, ts, http.MethodPost, "/auth", s.getAuthRequestRefresh(authResp.RefreshToken), authResp, "")
	authResp = ar.(*AuthResponse)
	is.Equal(res.StatusCode, http.StatusOK)
	t.Logf("%+v", authResp)

	// auth with wrong password
	authResp = &AuthResponse{}
	res, ar = testRequest(t, ts, http.MethodPost, "/auth", s.getAuthRequestWrongPw(), authResp, "")
	authResp = ar.(*AuthResponse)
	is.Equal(res.StatusCode, http.StatusUnauthorized)
	// t.Logf("%+v", authResp)

	// auth w  wrong refresh
	res, ar = testRequest(t, ts, http.MethodPost, "/auth", s.getAuthRequestWrongRefresh(authResp.RefreshToken), authResp, "")
	authResp = ar.(*AuthResponse)
	is.Equal(res.StatusCode, http.StatusUnauthorized)

	t.Logf("%+v", authResp)
}

func getProductReq(t *testing.T, name string) *bytes.Reader {
	is := is.New(t)
	prd := store.Product{
		MainImage: bucket.MainImage{
			Image: bucket.Image{
				FullSize: "https://main.com/img.jpg",
			},
		},
		Name: name,
		Price: &store.Price{
			USD: 1,
			BYN: 1,
			EUR: 1,
			RUB: 1,
		},
		AvailableSizes: &store.Size{
			XXS: 1,
			XS:  1,
			S:   1,
			M:   1,
			L:   1,
			XL:  1,
			XXL: 1,
			OS:  1,
		},
		ShortDescription:    "desc",
		DetailedDescription: []string{"1", "2"},
		Categories:          []string{"1", "2"},
		ProductImages: []bucket.Image{
			{
				FullSize: "https://ProductImages.com/img.jpg",
			},
			{
				FullSize: "https://ProductImages2.com/img.jpg",
			},
		},
	}

	prdBytes, err := json.Marshal(prd)
	is.NoErr(err)

	return bytes.NewReader(prdBytes)

}

func getArticleReq(t *testing.T, title string) *bytes.Reader {
	is := is.New(t)
	a := store.ArchiveArticle{
		Title:       title,
		Description: "desc",
		MainImage: bucket.MainImage{
			Image: bucket.Image{
				FullSize: "https://main.com/img.jpg",
			},
		},
		Content: []store.Content{
			{
				Image: bucket.Image{
					FullSize: "https://ProductImages.com/img.jpg",
				},
				MediaLink:    "https://MediaLink.com/img.jpg",
				Description:  "desc",
				TextPosition: "top",
			},
		},
	}

	aBytes, err := json.Marshal(a)
	is.NoErr(err)

	return bytes.NewReader(aBytes)

}

func TestProductsCRUDWAuth(t *testing.T) {
	is := is.New(t)

	db := buntFromConst()
	err := db.InitDB()
	is.NoErr(err)

	s := InitServerFromConst()

	ts := httptest.NewServer(s.Router())
	defer ts.Close()

	// jwt token
	authData, err := s.Auth.GetJWT()
	is.NoErr(err)
	t.Log(authData)

	// add product
	productResp := &ProductResponse{}
	name1 := "name1"
	res, pr := testRequest(t, ts, http.MethodPost, "/api/product", getProductReq(t, name1), productResp, authData.AccessToken)
	productResp = pr.(*ProductResponse)
	is.Equal(res.StatusCode, http.StatusOK)

	// modify product
	productResp2 := &ProductResponse{}
	name2 := "name2"
	res2, _ := testRequest(t, ts, http.MethodPut, fmt.Sprintf("/api/product/%d", productResp.Product.Id), getProductReq(t, name2), productResp2, authData.AccessToken)
	// productResp2 = mr.(*ProductResponse)
	is.Equal(res2.StatusCode, http.StatusOK)

	// get product by id
	productResp3 := &ProductResponse{}
	res3, gr := testRequest(t, ts, http.MethodGet, fmt.Sprintf("/api/product/%d", productResp.Product.Id), nil, productResp3, authData.AccessToken)
	productResp3 = gr.(*ProductResponse)
	is.Equal(res3.StatusCode, http.StatusOK)
	is.Equal(productResp3.Product.Name, name2)

	// delete by id
	productResp4 := &ProductResponse{}
	res4, dr := testRequest(t, ts, http.MethodDelete, fmt.Sprintf("/api/product/%d", productResp.Product.Id), nil, productResp4, authData.AccessToken)
	productResp4 = dr.(*ProductResponse)
	is.Equal(res4.StatusCode, http.StatusOK)

	t.Logf("%+v", productResp4)

	// get all
	allProductResp := &[]store.Product{}
	res5, ar := testRequest(t, ts, http.MethodGet, "/api/product", nil, allProductResp, authData.AccessToken)
	allProductResp = ar.(*[]store.Product)
	is.Equal(res5.StatusCode, http.StatusOK)
	is.Equal(len(*allProductResp), 0)
}

func TestArticlesCRUDWAuth(t *testing.T) {
	is := is.New(t)

	db := buntFromConst()
	err := db.InitDB()
	is.NoErr(err)

	s := InitServerFromConst()

	ts := httptest.NewServer(s.Router())
	defer ts.Close()

	// jwt token
	authData, err := s.Auth.GetJWT()
	is.NoErr(err)

	// add article
	articleResp := &ArticleResponse{}
	title1 := "title1"
	res, pr := testRequest(t, ts, http.MethodPost, "/api/archive", getArticleReq(t, title1), articleResp, authData.AccessToken)
	articleResp = pr.(*ArticleResponse)
	is.Equal(res.StatusCode, http.StatusOK)

	// modify article
	articleResp2 := &ArticleResponse{}
	title2 := "title2"
	res2, _ := testRequest(t, ts, http.MethodPut, fmt.Sprintf("/api/archive/%d", articleResp.ArchiveArticle.Id), getArticleReq(t, title2), articleResp2, authData.AccessToken)
	// articleResp2 = mr.(*ArticleResponse)
	is.Equal(res2.StatusCode, http.StatusOK)

	// get article by id
	articleResp3 := &ArticleResponse{}
	res3, gr := testRequest(t, ts, http.MethodGet, fmt.Sprintf("/api/archive/%d", articleResp.ArchiveArticle.Id), nil, articleResp3, authData.AccessToken)
	articleResp3 = gr.(*ArticleResponse)
	is.Equal(res3.StatusCode, http.StatusOK)
	is.Equal(articleResp3.ArchiveArticle.Title, title2)

	// delete article by id
	articleResp4 := &ArticleResponse{}
	res4, dr := testRequest(t, ts, http.MethodDelete, fmt.Sprintf("/api/archive/%d", articleResp.ArchiveArticle.Id), nil, articleResp4, authData.AccessToken)
	articleResp4 = dr.(*ArticleResponse)
	is.Equal(res4.StatusCode, http.StatusOK)

	t.Logf("%+v", articleResp4)

	// get all
	allArticleResp := &[]store.Product{}
	res5, ar := testRequest(t, ts, http.MethodGet, "/api/archive", nil, allArticleResp, authData.AccessToken)
	allArticleResp = ar.(*[]store.Product)
	is.Equal(res5.StatusCode, http.StatusOK)
	is.Equal(len(*allArticleResp), 0)
}
