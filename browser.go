package rod

import (
	"context"
	"sync"
	"time"

	"github.com/ysmood/kit"
	"github.com/ysmood/rod/lib/cdp"
)

// Browser represents the browser
type Browser struct {
	// ControlURL is the url to remote control browser.
	// If fails to connect to it, rod will try to open a local browser, if local browser is not found rod will try to download one.
	ControlURL string

	// Viewport is the default value to set after page creation
	// options: https://chromedevtools.github.io/devtools-protocol/tot/Emulation#method-setDeviceMetricsOverride
	Viewport *cdp.Object

	// Foreground enables the browser to run on foreground mode
	Foreground bool

	// Slowmotion delay each chrome control action
	Slowmotion time.Duration

	// Trace enables the visual tracing of the device input on the page
	Trace bool

	// OnFatal calls when a fatal error happens
	OnFatal func(error)

	ctx           context.Context
	timeoutCancel func()
	close         func()
	client        *cdp.Client
	event         *kit.Observable
	fatal         *kit.Observable
}

// OpenE ...
func (b *Browser) OpenE() (*Browser, error) {
	if b.ctx == nil {
		ctx, cancel := context.WithCancel(context.Background())
		b.ctx = ctx
		b.close = cancel
	}

	if _, err := cdp.GetWebSocketDebuggerURL(b.ControlURL); err != nil {
		args := cdp.ChromeArgs()
		if b.Foreground {
			delete(args, "--headless")
		}
		u, err := cdp.LaunchBrowser("", args)
		if err != nil {
			return nil, err
		}
		b.ControlURL = u
	}

	client, err := cdp.New(b.ctx, b.ControlURL)
	if err != nil {
		return nil, err
	}

	b.client = client

	return b, b.initEvents()
}

// Open a new browser controller
func Open(b *Browser) *Browser {
	if b == nil {
		b = &Browser{}
	}

	kit.E(b.OpenE())

	return b
}

// Ctx creates a clone with specified context
func (b *Browser) Ctx(ctx context.Context) *Browser {
	newObj := *b
	newObj.ctx = ctx
	return &newObj
}

// Timeout sets the timeout for chained sub-operations
func (b *Browser) Timeout(d time.Duration) *Browser {
	ctx, cancel := context.WithTimeout(b.ctx, d)
	b.timeoutCancel = cancel
	return b.Ctx(ctx)
}

// CloseE ...
func (b *Browser) CloseE() error {
	_, err := b.Call(&cdp.Message{Method: "Browser.close"})
	if err != nil {
		return err
	}

	if b.close != nil {
		b.close()
	}

	return nil
}

// Close the browser and release related resources
func (b *Browser) Close() {
	kit.E(b.CloseE())
}

// PageE ...
func (b *Browser) PageE(url string) (*Page, error) {
	target, err := b.Call(&cdp.Message{
		Method: "Target.createTarget",
		Params: cdp.Object{
			"url": url,
		},
	})
	if err != nil {
		return nil, err
	}

	return b.page(target.Get("targetId").String())
}

// Page creates a new page
func (b *Browser) Page(url string) *Page {
	p, err := b.PageE(url)
	kit.E(err)
	return p
}

// PagesE ...
func (b *Browser) PagesE() ([]*Page, error) {
	list, err := b.Call(&cdp.Message{Method: "Target.getTargets"})
	if err != nil {
		return nil, err
	}

	pageList := []*Page{}
	for _, target := range list.Get("targetInfos").Array() {
		if target.Get("type").String() != "page" {
			continue
		}

		page, err := b.page(target.Get("targetId").String())
		if err != nil {
			return nil, err
		}
		pageList = append(pageList, page)
	}

	return pageList, nil
}

// Pages returns all visible pages
func (b *Browser) Pages() []*Page {
	list, err := b.PagesE()
	kit.E(err)
	return list
}

// WaitEventE ...
func (b *Browser) WaitEventE(name string) (kit.JSONResult, error) {
	msg, err := b.Event().Until(b.ctx, func(e kit.Event) bool {
		return e.(*cdp.Message).Method == name
	})
	if err != nil {
		return nil, err
	}
	return kit.JSON(kit.MustToJSON(msg.(*cdp.Message).Params)), nil
}

// WaitEvent waits for the next event to happen.
// Example event names: Page.javascriptDialogOpening, Page.frameNavigated, DOM.attributeModified
func (b *Browser) WaitEvent(name string) kit.JSONResult {
	res, err := b.WaitEventE(name)
	kit.E(err)
	return res
}

// Call sends a control message to browser
func (b *Browser) Call(msg *cdp.Message) (kit.JSONResult, error) {
	b.slowmotion(msg.Method)

	return b.client.Call(b.ctx, msg)
}

// Event returns the observable for browser events
func (b *Browser) Event() *kit.Observable {
	return b.event
}

func (b *Browser) page(targetID string) (*Page, error) {
	page := &Page{
		ctx:                 b.ctx,
		browser:             b,
		TargetID:            targetID,
		getDownloadFileLock: &sync.Mutex{},
	}

	page.Mouse = &Mouse{page: page}

	page.Keyboard = &Keyboard{page: page}

	return page, page.initSession()
}

func (b *Browser) initEvents() error {
	b.event = kit.NewObservable()
	b.fatal = kit.NewObservable()

	go func() {
		for msg := range b.client.Event() {
			go b.event.Publish(msg)
		}
	}()

	go func() {
		for err := range b.fatal.Subscribe().C {
			if b.OnFatal == nil {
				kit.Err(kit.Sdump(err))
			} else {
				b.OnFatal(err.(error))
			}
		}
	}()

	_, err := b.Call(&cdp.Message{
		Method: "Target.setDiscoverTargets",
		Params: cdp.Object{"discover": true},
	})

	return err
}
