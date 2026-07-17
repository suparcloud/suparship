package onepassword

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FakeClient implements SAClient for testing. All state is in-memory.
type FakeClient struct {
	mu            sync.Mutex
	Vaults        map[string]VaultInfo
	Items         map[string]map[string]Item // vaultID -> itemID -> Item
	GroupGrants   map[string]Permission      // "vaultID:groupID" -> perms
	ConnectTokens map[string]ConnectToken    // tokenID -> token
	nextID        int
	ProbeErr      error
	CreateErr     error
	DeleteErr     error
	GrantErr      error
	IssueErr      error
	RevokeErr     error
	UpsertErr     error
	GetItemErr    error
}

// NewFakeClient creates a FakeClient with empty state.
func NewFakeClient() *FakeClient {
	return &FakeClient{
		Vaults:        make(map[string]VaultInfo),
		Items:         make(map[string]map[string]Item),
		GroupGrants:   make(map[string]Permission),
		ConnectTokens: make(map[string]ConnectToken),
	}
}

func (f *FakeClient) genID() string {
	f.nextID++
	return fmt.Sprintf("fake-%d", f.nextID)
}

func (f *FakeClient) Probe(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ProbeErr != nil {
		return 0, f.ProbeErr
	}
	return len(f.Vaults), nil
}

func (f *FakeClient) CreateVault(_ context.Context, title, description string) (VaultInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return VaultInfo{}, f.CreateErr
	}

	// Idempotent: check existing by title.
	for _, v := range f.Vaults {
		if v.Title == title {
			return v, nil
		}
	}

	info := VaultInfo{
		ID:          f.genID(),
		Title:       title,
		Description: description,
		CreatedAt:   time.Now(),
	}
	f.Vaults[info.ID] = info
	f.Items[info.ID] = make(map[string]Item)
	return info, nil
}

func (f *FakeClient) ListVaults(_ context.Context) ([]VaultInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]VaultInfo, 0, len(f.Vaults))
	for _, v := range f.Vaults {
		out = append(out, v)
	}
	return out, nil
}

func (f *FakeClient) GetVault(_ context.Context, vaultID string) (VaultInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	v, ok := f.Vaults[vaultID]
	if !ok {
		return VaultInfo{}, ErrVaultNotFound
	}
	return v, nil
}

func (f *FakeClient) DeleteVault(_ context.Context, vaultID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	if _, ok := f.Vaults[vaultID]; !ok {
		return ErrVaultNotFound
	}
	delete(f.Vaults, vaultID)
	delete(f.Items, vaultID)
	return nil
}

func (f *FakeClient) GrantGroupAccess(_ context.Context, vaultID, groupID string, perms Permission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GrantErr != nil {
		return f.GrantErr
	}
	if _, ok := f.Vaults[vaultID]; !ok {
		return ErrVaultNotFound
	}
	f.GroupGrants[vaultID+":"+groupID] = perms
	return nil
}

func (f *FakeClient) RevokeGroupAccess(_ context.Context, vaultID, groupID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Vaults[vaultID]; !ok {
		return ErrVaultNotFound
	}
	delete(f.GroupGrants, vaultID+":"+groupID)
	return nil
}

func (f *FakeClient) IssueConnectToken(_ context.Context, serverName string, vaultIDs []string, label string) (ConnectToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.IssueErr != nil {
		return ConnectToken{}, f.IssueErr
	}

	tok := ConnectToken{
		ID:       f.genID(),
		Token:    fmt.Sprintf("fake-connect-token-%s-%d", label, f.nextID),
		ServerID: serverName,
	}
	f.ConnectTokens[tok.ID] = tok
	return tok, nil
}

// HasConnectToken reports whether a connect token exists. Thread-safe: the
// provisioner revokes rotated tokens from a background goroutine, so tests must
// not read the ConnectTokens map directly (that races the revoke).
func (f *FakeClient) HasConnectToken(tokenID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.ConnectTokens[tokenID]
	return ok
}

func (f *FakeClient) RevokeConnectToken(_ context.Context, tokenID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RevokeErr != nil {
		return f.RevokeErr
	}
	if _, ok := f.ConnectTokens[tokenID]; !ok {
		return fmt.Errorf("onepassword: connect token %q not found", tokenID)
	}
	delete(f.ConnectTokens, tokenID)
	return nil
}

func (f *FakeClient) UpsertItem(_ context.Context, vaultID, title string, fields []ItemField) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.UpsertErr != nil {
		return "", f.UpsertErr
	}
	if _, ok := f.Vaults[vaultID]; !ok {
		return "", ErrVaultNotFound
	}

	vaultItems := f.Items[vaultID]
	// Idempotent: find by title.
	for id, item := range vaultItems {
		if item.Title == title {
			item.Fields = fields
			vaultItems[id] = item
			return id, nil
		}
	}

	id := f.genID()
	vaultItems[id] = Item{
		ID:      id,
		VaultID: vaultID,
		Title:   title,
		Fields:  fields,
	}
	return id, nil
}

func (f *FakeClient) GetItem(_ context.Context, vaultID, itemID string) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetItemErr != nil {
		return Item{}, f.GetItemErr
	}
	vaultItems, ok := f.Items[vaultID]
	if !ok {
		return Item{}, ErrVaultNotFound
	}
	item, ok := vaultItems[itemID]
	if !ok {
		return Item{}, ErrItemNotFound
	}
	return item, nil
}

func (f *FakeClient) ListItems(_ context.Context, vaultID string) ([]ItemOverview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vaultItems, ok := f.Items[vaultID]
	if !ok {
		return nil, ErrVaultNotFound
	}
	out := make([]ItemOverview, 0, len(vaultItems))
	for _, item := range vaultItems {
		out = append(out, ItemOverview{
			ID:      item.ID,
			VaultID: vaultID,
			Title:   item.Title,
		})
	}
	return out, nil
}

func (f *FakeClient) DeleteItem(_ context.Context, vaultID, itemID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	vaultItems, ok := f.Items[vaultID]
	if !ok {
		return ErrVaultNotFound
	}
	if _, ok := vaultItems[itemID]; !ok {
		return ErrItemNotFound
	}
	delete(vaultItems, itemID)
	return nil
}

// Compile-time check.
var _ SAClient = (*FakeClient)(nil)
