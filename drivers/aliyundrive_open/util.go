package aliyundrive_open

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/deatil/go-cryptobin/cryptobin/crypto"
	"net/http"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

// do others that not defined in Driver interface

func (d *AliyundriveOpen) _refreshToken() (string, string, error) {
	url := API_URL + "/oauth/access_token"
	if d.OauthTokenURL != "" && d.ClientID == "" {
		url = d.OauthTokenURL
	}
	//var resp base.TokenResp
	var e ErrResp
	res, err := base.RestyClient.R().
		//ForceContentType("application/json").
		SetBody(base.Json{
			"client_id":     d.ClientID,
			"client_secret": d.ClientSecret,
			"grant_type":    "refresh_token",
			"refresh_token": d.RefreshToken,
		}).
		//SetResult(&resp).
		SetError(&e).
		Post(url)
	if err != nil {
		return "", "", err
	}
	log.Debugf("[ali_open] refresh token response: %s", res.String())
	if e.Code != "" {
		return "", "", fmt.Errorf("failed to refresh token: %s", e.Message)
	}
	refresh, access := utils.Json.Get(res.Body(), "refresh_token").ToString(), utils.Json.Get(res.Body(), "access_token").ToString()
	if refresh == "" {
		return "", "", fmt.Errorf("failed to refresh token: refresh token is empty, resp: %s", res.String())
	}
	curSub, err := getSub(d.RefreshToken)
	if err != nil {
		return "", "", err
	}
	newSub, err := getSub(refresh)
	if err != nil {
		return "", "", err
	}
	if curSub != newSub {
		return "", "", errors.New("failed to refresh token: sub not match")
	}
	return refresh, access, nil
}

func getSub(token string) (string, error) {
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return "", errors.New("not a jwt token because of invalid segments")
	}
	bs, err := base64.RawStdEncoding.DecodeString(segments[1])
	if err != nil {
		return "", errors.New("failed to decode jwt token")
	}
	return utils.Json.Get(bs, "sub").ToString(), nil
}

func (d *AliyundriveOpen) refreshToken() error {
	if d.ref != nil {
		return d.ref.refreshToken()
	}
	refresh, access, err := d._refreshToken()
	for i := 0; i < 3; i++ {
		if err == nil {
			break
		} else {
			log.Errorf("[ali_open] failed to refresh token: %s", err)
		}
		refresh, access, err = d._refreshToken()
	}
	if err != nil {
		return err
	}
	log.Infof("[ali_open] token exchange: %s -> %s", d.RefreshToken, refresh)
	d.RefreshToken, d.AccessToken = refresh, access
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *AliyundriveOpen) request(uri, method string, callback base.ReqCallback, retry ...bool) ([]byte, error) {
	b, err, _ := d.requestReturnErrResp(uri, method, callback, retry...)
	return b, err
}

func (d *AliyundriveOpen) requestReturnErrResp(uri, method string, callback base.ReqCallback, retry ...bool) ([]byte, error, *ErrResp) {
	req := base.RestyClient.R()
	// TODO check whether access_token is expired
	req.SetHeader("Authorization", "Bearer "+d.getAccessToken())
	if method == http.MethodPost {
		req.SetHeader("Content-Type", "application/json")
	}
	if callback != nil {
		callback(req)
	}
	var e ErrResp
	req.SetError(&e)
	res, err := req.Execute(method, API_URL+uri)
	if err != nil {
		if res != nil {
			log.Errorf("[aliyundrive_open] request error: %s", res.String())
		}
		return nil, err, nil
	}
	isRetry := len(retry) > 0 && retry[0]
	if e.Code != "" {
		if !isRetry && (utils.SliceContains([]string{"AccessTokenInvalid", "AccessTokenExpired", "I400JD"}, e.Code) || d.AccessToken == "") {
			if d.UseTVAuth {
				err = d.getRefreshTokenByTV(d.RefreshToken, true)
			} else {
				err = d.refreshToken()
			}
			if err != nil {
				return nil, err, nil
			}
			return d.requestReturnErrResp(uri, method, callback, true)
		}
		return nil, fmt.Errorf("%s:%s", e.Code, e.Message), &e
	}
	return res.Body(), nil, nil
}

func (d *AliyundriveOpen) list(ctx context.Context, data base.Json) (*Files, error) {
	var resp Files
	_, err := d.request("/adrive/v1.0/openFile/list", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetResult(&resp)
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (d *AliyundriveOpen) getFiles(ctx context.Context, fileId string) ([]File, error) {
	marker := "first"
	res := make([]File, 0)
	for marker != "" {
		if marker == "first" {
			marker = ""
		}
		data := base.Json{
			"drive_id":        d.DriveId,
			"limit":           200,
			"marker":          marker,
			"order_by":        d.OrderBy,
			"order_direction": d.OrderDirection,
			"parent_file_id":  fileId,
			//"category":              "",
			//"type":                  "",
			//"video_thumbnail_time":  120000,
			//"video_thumbnail_width": 480,
			//"image_thumbnail_width": 480,
		}
		resp, err := d.limitList(ctx, data)
		if err != nil {
			return nil, err
		}
		marker = resp.NextMarker
		res = append(res, resp.Items...)
	}
	return res, nil
}

func getNowTime() (time.Time, string) {
	nowTime := time.Now()
	nowTimeStr := nowTime.Format("2006-01-02T15:04:05.000Z")
	return nowTime, nowTimeStr
}

func (d *AliyundriveOpen) getAccessToken() string {
	if d.ref != nil {
		return d.ref.getAccessToken()
	}
	return d.AccessToken
}

func (d *AliyundriveOpen) getSID() error {
	url := "http://api.extscreen.com/aliyundrive/qrcode"
	var resp SIDResp
	_, err := base.RestyClient.R().
		SetBody(base.Json{
			"scopes": "user:base,file:all:read,file:all:write",
			"width":  500,
			"height": 500,
		}).
		SetResult(&resp).
		Post(url)
	if err != nil {
		return err
	}
	d.SID = resp.Data.SID
	op.MustSaveDriverStorage(d)
	authURL := fmt.Sprintf("https://www.aliyundrive.com/o/oauth/authorize?sid=%s", resp.Data.SID)
	return fmt.Errorf(`need verify: <a target="_blank" href="%s">Click Here</a>`, authURL)
}

func (d *AliyundriveOpen) getRefreshTokenBySID() error {
	// 获取 authCode
	authCode := ""
	url := "https://openapi.alipan.com/oauth/qrcode/" + d.SID + "/status"
	time.Sleep(time.Second) // 等待 阿里云盘那边更新SID状态
	var resp RefreshTokenSIDResp
	_, err := base.RestyClient.R().
		SetResult(&resp).
		Get(url)
	if err != nil {
		return err
	}
	if resp.Status != "LoginSuccess" {
		return fmt.Errorf("failed to get auth code: %s", resp.Status)

	} else {
		authCode = resp.AuthCode
	}
	return d.getRefreshTokenByTV(authCode, false)
}

func (d *AliyundriveOpen) getRefreshTokenByTV(code string, isRefresh bool) error {
	refresh, access, err := d._getRefreshTokenByTV(code, isRefresh)
	for i := 0; i < 3; i++ {
		if err == nil {
			break
		} else {
			log.Errorf("[ali_open] failed to refresh token: %s", err)
		}
		refresh, access, err = d._getRefreshTokenByTV(code, isRefresh)
	}
	if err != nil {
		return err
	}
	log.Infof("[ali_open] token exchange: %s -> %s", d.RefreshToken, refresh)
	d.RefreshToken, d.AccessToken = refresh, access
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *AliyundriveOpen) _getRefreshTokenByTV(code string, isRefresh bool) (refreshToken, accessToken string, err error) {
	url := "http://api.extscreen.com/aliyundrive/v2/token"
	var resp RefreshTokenAuthResp
	body := ""
	if isRefresh {
		body = fmt.Sprintf("refresh_token=%s", code)
	} else {
		body = fmt.Sprintf("code=%s", code)
	}

	res, err := base.RestyClient.R().
		SetHeader("Content-Type", "application/x-www-form-urlencoded").
		SetBody(body).
		SetResult(&resp).
		Post(url)
	if err != nil {
		return "", "", err
	}
	v, err := _decryptForTV(resp.Data.CipherText, resp.Data.IV)
	if err != nil {
		return "", "", err
	}
	refresh, access := v.RefreshToken, v.AccessToken
	if refresh == "" {
		return "", "", fmt.Errorf("failed to refresh token: refresh token is empty, resp: %s", res.String())
	}
	return refresh, access, nil
}

func _decryptForTV(cipherText, iv string) (*ResData, error) {
	cipher, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return nil, err
	}
	iV, err := hex.DecodeString(iv)
	if err != nil {
		return nil, err
	}
	res := crypto.FromBytes(cipher).SetKey("^(i/x>>5(ebyhumz*i1wkpk^orIs^Na.").SetIv(string(iV)).Aes().CBC().PKCS7Padding().Decrypt()
	fmt.Println(string(res.ToBytes()))
	fmt.Println(res.Error())
	v := new(ResData)
	if err := json.Unmarshal(res.ToBytes(), v); err != nil {
		return nil, err

	}
	return v, nil
}

// Remove duplicate files with the same name in the given directory path,
// preserving the file with the given skipID if provided
func (d *AliyundriveOpen) removeDuplicateFiles(ctx context.Context, parentPath string, fileName string, skipID string) error {
	// Handle empty path (root directory) case
	if parentPath == "" {
		parentPath = "/"
	}

	// List all files in the parent directory
	files, err := op.List(ctx, d, parentPath, model.ListArgs{})
	if err != nil {
		return err
	}

	// Find all files with the same name
	var duplicates []model.Obj
	for _, file := range files {
		if file.GetName() == fileName && file.GetID() != skipID {
			duplicates = append(duplicates, file)
		}
	}

	// Remove all duplicates files, except the file with the given ID
	for _, file := range duplicates {
		err := d.Remove(ctx, file)
		if err != nil {
			return err
		}
	}

	return nil
}
