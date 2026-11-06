package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultClientID = "4a07b708-b86d-4365-a55f-f4f23ecb85ab"
const msaScope = "XboxLive.signin offline_access"
const tokenURL = "https://login.microsoftonline.com/consumers/oauth2/v2.0/token"

var xstsErrors = map[int64]string{
	2148916233: "У этого Microsoft-аккаунта нет профиля Xbox. Зайди на xbox.com, создай его и повтори.",
	2148916235: "Аккаунт забанен в Xbox Live.",
	2148916236: "Нужно подтверждение возраста на аккаунте Microsoft.",
	2148916238: "Аккаунту нет 18, нужно разрешение взрослого в семье Microsoft.",
}

type profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	XUID string `json:"xuid"`
}

type account struct {
	ClientID     string  `json:"clientId"`
	RefreshToken string  `json:"refreshToken"`
	MCToken      string  `json:"mcToken"`
	ExpiresAt    int64   `json:"expiresAt"`
	Profile      profile `json:"profile"`
}

func accountPath() string { return filepath.Join(dataDir(), "account.json") }

func readAccount() *account {
	b, err := os.ReadFile(accountPath())
	if err != nil {
		return nil
	}
	var a account
	if json.Unmarshal(b, &a) != nil || a.Profile.Name == "" {
		return nil
	}
	return &a
}

func clearAccount() { _ = os.Remove(accountPath()) }

func saveAccount(a account) error {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return os.WriteFile(accountPath(), b, 0o600)
}

func postForm(rawURL string, params url.Values) (int, map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(params.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TBSLauncher/"+launcherVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var data map[string]any
	_ = json.Unmarshal(body, &data)
	return resp.StatusCode, data, nil
}

func postJSON(rawURL string, payload any, headers map[string]string) (int, []byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(string(b)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TBSLauncher/"+launcherVersion)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}

type deviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Message         string `json:"message"`
	Interval        int    `json:"interval"`
}

func startDeviceCode(clientID string) (deviceCode, error) {
	if clientID == "" {
		clientID = defaultClientID
	}
	status, data, err := postForm("https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode", url.Values{
		"client_id": {clientID},
		"scope":     {msaScope},
	})
	if err != nil {
		return deviceCode{}, err
	}
	b, _ := json.Marshal(data)
	var dc deviceCode
	_ = json.Unmarshal(b, &dc)
	if status < 200 || status >= 300 || dc.DeviceCode == "" || dc.UserCode == "" {
		msg := str(data["error_description"])
		if msg == "" {
			msg = str(data["error"])
		}
		if msg == "" {
			msg = "Microsoft не выдал код входа"
		}
		return deviceCode{}, fmt.Errorf("%s", msg)
	}
	if dc.VerificationURI == "" {
		dc.VerificationURI = "https://www.microsoft.com/link"
	}
	if dc.Interval < 5 {
		dc.Interval = 5
	}
	return dc, nil
}

type msaToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func pollDeviceCode(clientID, device string) (done bool, token msaToken, err error) {
	status, data, err := postForm(tokenURL, url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {clientID},
		"device_code": {device},
	})
	if err != nil {
		return false, msaToken{}, err
	}
	b, _ := json.Marshal(data)
	var t msaToken
	_ = json.Unmarshal(b, &t)
	if status >= 200 && status < 300 && t.AccessToken != "" {
		return true, t, nil
	}
	if t.Error == "authorization_pending" || t.Error == "slow_down" {
		return false, msaToken{}, nil
	}
	msg := t.Description
	if msg == "" {
		msg = t.Error
	}
	if msg == "" {
		msg = "вход отменён"
	}
	return false, msaToken{}, fmt.Errorf("%s", msg)
}

func xboxChain(msaAccess string) (access string, expires int64, xuid string, err error) {
	_, body, err := postJSON("https://user.auth.xboxlive.com/user/authenticate", map[string]any{
		"Properties": map[string]any{
			"AuthMethod": "RPS",
			"SiteName":   "user.auth.xboxlive.com",
			"RpsTicket":  "d=" + msaAccess,
		},
		"RelyingParty": "http://auth.xboxlive.com",
		"TokenType":    "JWT",
	}, nil)
	if err != nil {
		return "", 0, "", err
	}
	var xbl struct {
		Token         string `json:"Token"`
		DisplayClaims struct {
			Xui []struct {
				Uhs string `json:"uhs"`
				Xid string `json:"xid"`
			} `json:"xui"`
		} `json:"DisplayClaims"`
	}
	if json.Unmarshal(body, &xbl) != nil || xbl.Token == "" || len(xbl.DisplayClaims.Xui) == 0 || xbl.DisplayClaims.Xui[0].Uhs == "" {
		return "", 0, "", fmt.Errorf("Xbox Live не принял аккаунт")
	}
	uhs := xbl.DisplayClaims.Xui[0].Uhs
	xuid = xbl.DisplayClaims.Xui[0].Xid
	status, body, err := postJSON("https://xsts.auth.xboxlive.com/xsts/authorize", map[string]any{
		"Properties": map[string]any{
			"SandboxId":  "RETAIL",
			"UserTokens": []string{xbl.Token},
		},
		"RelyingParty": "rp://api.minecraftservices.com/",
		"TokenType":    "JWT",
	}, nil)
	if err != nil {
		return "", 0, "", err
	}
	var xsts struct {
		Token         string `json:"Token"`
		XErr          int64  `json:"XErr"`
		DisplayClaims struct {
			Xui []struct {
				Xid string `json:"xid"`
			} `json:"xui"`
		} `json:"DisplayClaims"`
	}
	_ = json.Unmarshal(body, &xsts)
	if status < 200 || status >= 300 || xsts.Token == "" {
		if msg, ok := xstsErrors[xsts.XErr]; ok {
			return "", 0, "", fmt.Errorf("%s", msg)
		}
		return "", 0, "", fmt.Errorf("XSTS отклонил аккаунт")
	}
	if len(xsts.DisplayClaims.Xui) > 0 && xsts.DisplayClaims.Xui[0].Xid != "" {
		xuid = xsts.DisplayClaims.Xui[0].Xid
	}
	status, body, err = postJSON("https://api.minecraftservices.com/authentication/login_with_xbox", map[string]any{
		"identityToken": "XBL3.0 x=" + uhs + ";" + xsts.Token,
	}, nil)
	if err != nil {
		return "", 0, "", err
	}
	var mc struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	_ = json.Unmarshal(body, &mc)
	if status == 403 || (status >= 400 && strings.Contains(strings.ToLower(string(body)), "invalid app")) {
		return "", 0, "", fmt.Errorf("Microsoft не пустил этот лаунчер к лицензии. В настройках вставь свой Azure Client ID и повтори вход")
	}
	if status < 200 || status >= 300 || mc.AccessToken == "" {
		return "", 0, "", fmt.Errorf("Minecraft не выдал сессию")
	}
	if xuid == "" {
		xuid = "0"
	}
	if mc.ExpiresIn == 0 {
		mc.ExpiresIn = 86400
	}
	return mc.AccessToken, mc.ExpiresIn, xuid, nil
}

func fetchProfile(access string) (profile, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.minecraftservices.com/minecraft/profile", nil)
	if err != nil {
		return profile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("User-Agent", "TBSLauncher/"+launcherVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return profile{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == 404 {
		return profile{}, fmt.Errorf("На этом аккаунте нет лицензии Minecraft Java. Пиратка и оффлайн-ник не пройдут")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return profile{}, fmt.Errorf("Не удалось проверить лицензию")
	}
	var p struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(body, &p) != nil || p.ID == "" || p.Name == "" {
		return profile{}, fmt.Errorf("Профиль Minecraft пустой")
	}
	return profile{ID: p.ID, Name: p.Name}, nil
}

func finishLogin(msa msaToken, clientID string) (account, error) {
	if clientID == "" {
		clientID = defaultClientID
	}
	access, expires, xuid, err := xboxChain(msa.AccessToken)
	if err != nil {
		return account{}, err
	}
	prof, err := fetchProfile(access)
	if err != nil {
		return account{}, err
	}
	prof.XUID = xuid
	a := account{
		ClientID:     clientID,
		RefreshToken: msa.RefreshToken,
		MCToken:      access,
		ExpiresAt:    time.Now().UnixMilli() + expires*1000,
		Profile:      prof,
	}
	if err := saveAccount(a); err != nil {
		return account{}, err
	}
	return a, nil
}

func refreshMSA(a account) (msaToken, error) {
	clientID := a.ClientID
	if clientID == "" {
		clientID = defaultClientID
	}
	status, data, err := postForm(tokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {a.RefreshToken},
		"scope":         {msaScope},
	})
	if err != nil {
		return msaToken{}, err
	}
	b, _ := json.Marshal(data)
	var t msaToken
	_ = json.Unmarshal(b, &t)
	if status < 200 || status >= 300 || t.AccessToken == "" {
		clearAccount()
		return msaToken{}, fmt.Errorf("Сессия Microsoft протухла. Привяжи лицензию ещё раз")
	}
	if t.RefreshToken == "" {
		t.RefreshToken = a.RefreshToken
	}
	return t, nil
}

func ensureFreshAccount() (account, error) {
	a := readAccount()
	if a == nil || a.RefreshToken == "" {
		return account{}, fmt.Errorf("Сначала привяжи лицензию Minecraft")
	}
	if a.MCToken != "" && a.ExpiresAt-time.Now().UnixMilli() > 5*60*1000 {
		if _, err := fetchProfile(a.MCToken); err == nil {
			return *a, nil
		}
	}
	msa, err := refreshMSA(*a)
	if err != nil {
		return account{}, err
	}
	clientID := a.ClientID
	if clientID == "" {
		clientID = defaultClientID
	}
	return finishLogin(msa, clientID)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
