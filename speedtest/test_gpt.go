package speedtest

import (
	"encoding/json"
	C "github.com/metacubex/mihomo/constant"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type response struct {
	CFDetails string `json:"cf_details"`
}

type gptTest struct {
	proxy   C.Proxy
	timeout time.Duration
	//
	client *http.Client
}

func newGPTTest(proxy C.Proxy, timeout time.Duration) *gptTest {
	client := getClient(proxy, timeout)
	return &gptTest{
		proxy:   proxy,
		timeout: timeout,
		client:  client,
	}
}

func (g *gptTest) requestChatGPT(url string) (*response, error) {
	client := g.client
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	if err != nil {
		return nil, err
	}

	var data response
	if err = json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return &data, err
}

func (g *gptTest) testWeb() (bool, string) {
	// 1. 访问 https://chat.openai.com/cdn-cgi/trace 获取 loc= 国家码，查看是否支持
	// 2. 访问 https://chat.openai.com/ 如果无响应就不可用
	client := g.client
	//
	resp, err := client.Get(GPTTrace)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return false, ""
	}
	lines := strings.Split(string(body), "\n")
	var loc string
	for _, line := range lines {
		if !strings.Contains(line, "loc=") {
			continue
		}
		loc = strings.Replace(line, "loc=", "", 1)
		loc = strings.ToUpper(loc)
		break
	}
	for _, c := range gptSupportCountry {
		if strings.Compare(loc, c) == 0 {
			return true, loc
		}
	}
	//
	resp, err = client.Get(GPTTestURLWeb)
	if err != nil {
		return false, loc
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return false, loc
	}

	return false, loc
}

func (g *gptTest) Test() GPTResult {
	type gptResultSync struct {
		Android atomic.Bool
		IOS     atomic.Bool
		Web     atomic.Bool
		Loc     atomic.Value
	}

	var res = gptResultSync{}

	wg := sync.WaitGroup{}

	wg.Add(3)

	// ● 若不可用会提示
	// {"cf_details":"Something went wrong. You may be connected to a disallowed ISP. If you are using VPN, try disabling it. Otherwise try a different Wi-Fi network or data connection."}
	//
	// ● 可用提示
	// {"cf_details":"Request is not allowed. Please try again later.", "type":"dc"}
	go func() {
		defer wg.Done()
		if resp, err := g.requestChatGPT(GPTTestURLAndroid); err == nil {
			if !strings.Contains(resp.CFDetails, errMsg) {
				res.Android.Store(true)
			}
		}
	}()

	go func() {
		defer wg.Done()
		if resp, err := g.requestChatGPT(GPTTestURLIOS); err == nil {
			if !strings.Contains(resp.CFDetails, errMsg) {
				res.IOS.Store(true)
			}
		}

	}()

	go func() {
		defer wg.Done()
		ok, loc := g.testWeb()
		res.Web.Store(ok)
		res.Loc.Store(loc)
	}()

	wg.Wait()

	var r = GPTResult{
		Android: res.Android.Load(),
		IOS:     res.IOS.Load(),
		Web:     res.Web.Load(),
		Loc:     res.Loc.Load().(string),
	}
	if !r.Web {
		r.Android = r.Web
		r.IOS = r.Web
	}
	return r
}

func TestChatGPTAccess(proxy C.Proxy, timeout time.Duration) GPTResult {
	t := newGPTTest(proxy, timeout)
	return t.Test()
}

func TestURLAvailable(urls []string, proxy C.Proxy, timeout time.Duration) map[string]bool {
	if len(urls) == 0 {
		return nil
	}
	var res = sync.Map{}
	wg := sync.WaitGroup{}
	wg.Add(len(urls))

	for _, url := range urls {

		go func(url string) {
			defer wg.Done()
			client := getClient(proxy, timeout)
			resp, err := client.Get(url)
			if err != nil {
				res.Store(url, false)
				return
			}
			if resp.StatusCode-http.StatusOK > 200 {
				res.Store(url, false)
				return
			}
			res.Store(url, true)
		}(url)
	}
	wg.Wait()
	var result = make(map[string]bool)
	res.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(bool)
		return true
	})
	return result
}
