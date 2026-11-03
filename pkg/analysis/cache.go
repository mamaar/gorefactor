package analysis

import (
	"strings"
	"sync"

	"github.com/mamaar/gorefactor/pkg/types"
)

// SymbolCache provides efficient caching for symbol resolution operations
type SymbolCache struct {
	// Cache for resolved symbol references
	resolvedRefs map[string]*types.Symbol
	refsLock     sync.RWMutex

	// Cache for method sets
	methodSets  map[string][]*types.Symbol
	methodsLock sync.RWMutex

	// NEW: Cache for identifier type resolutions (file:pos -> type symbol)
	identifierTypes map[string]*types.Symbol
	typesLock       sync.RWMutex
}

// NewSymbolCache creates a new symbol cache
func NewSymbolCache() *SymbolCache {
	return &SymbolCache{
		resolvedRefs:    make(map[string]*types.Symbol),
		methodSets:      make(map[string][]*types.Symbol),
		identifierTypes: make(map[string]*types.Symbol), // NEW
	}
}

// Resolved reference caching

// GetResolvedRef retrieves a cached resolved symbol reference
func (c *SymbolCache) GetResolvedRef(key string) *types.Symbol {
	c.refsLock.RLock()
	defer c.refsLock.RUnlock()

	return c.resolvedRefs[key]
}

// SetResolvedRef caches a resolved symbol reference
func (c *SymbolCache) SetResolvedRef(key string, symbol *types.Symbol) {
	c.refsLock.Lock()
	defer c.refsLock.Unlock()

	c.resolvedRefs[key] = symbol
}

// Method set caching

// GetMethodSet retrieves a cached method set
func (c *SymbolCache) GetMethodSet(key string) []*types.Symbol {
	c.methodsLock.RLock()
	defer c.methodsLock.RUnlock()

	return c.methodSets[key]
}

// SetMethodSet caches a method set
func (c *SymbolCache) SetMethodSet(key string, methods []*types.Symbol) {
	c.methodsLock.Lock()
	defer c.methodsLock.Unlock()

	c.methodSets[key] = methods
}

// Identifier type caching (NEW)

// GetIdentifierType retrieves a cached identifier type resolution
func (c *SymbolCache) GetIdentifierType(key string) *types.Symbol {
	c.typesLock.RLock()
	defer c.typesLock.RUnlock()

	return c.identifierTypes[key]
}

// SetIdentifierType caches an identifier type resolution
func (c *SymbolCache) SetIdentifierType(key string, symbol *types.Symbol) {
	c.typesLock.Lock()
	defer c.typesLock.Unlock()

	c.identifierTypes[key] = symbol
}

// Method-to-type mapping caching (NEW)

// Package symbol table caching

// Cache invalidation

// InvalidatePackage removes all cache entries related to a package
func (c *SymbolCache) InvalidatePackage(packagePath string) {
	// Remove resolved references for the package
	c.refsLock.Lock()
	for key := range c.resolvedRefs {
		if strings.Contains(key, packagePath) {
			delete(c.resolvedRefs, key)
		}
	}
	c.refsLock.Unlock()

	// Remove method sets for the package
	c.methodsLock.Lock()
	for key := range c.methodSets {
		if strings.HasPrefix(key, "methodset:"+packagePath) {
			delete(c.methodSets, key)
		}
	}
	c.methodsLock.Unlock()
}

// InvalidateFile removes cache entries related to a specific file
func (c *SymbolCache) InvalidateFile(filePath string) {
	// Remove resolved references for the file
	c.refsLock.Lock()
	for key := range c.resolvedRefs {
		if strings.HasPrefix(key, filePath+":") {
			delete(c.resolvedRefs, key)
		}
	}
	c.refsLock.Unlock()
}

// Cache statistics

// Cache efficiency methods

// Private helper methods for statistics

// Cache warming methods

// Maintenance methods
