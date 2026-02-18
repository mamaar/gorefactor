package analysis

import (
	"strings"
	"sync"
	"time"

	"github.com/mamaar/gorefactor/pkg/types"
)

// SymbolCache provides efficient caching for symbol resolution operations
type SymbolCache struct {
	// Cache for resolved symbol references
	resolvedRefs map[string]*types.Symbol
	refsLock     sync.RWMutex

	// Cache for method sets
	methodSets map[string][]*types.Symbol
	methodsLock sync.RWMutex

	// Cache for package symbol tables
	packageSymbols map[string]*types.SymbolTable
	packagesLock   sync.RWMutex

	// NEW: Cache for identifier type resolutions (file:pos -> type symbol)
	identifierTypes map[string]*types.Symbol
	typesLock       sync.RWMutex

	// NEW: Cache for method-to-type mappings (type:method -> bool)
	methodTypeMap map[string]bool
	methodTypeMapLock sync.RWMutex

	// Cache metadata
	cacheStats CacheStats
	statsLock  sync.RWMutex
}

// CacheStats tracks cache performance metrics
type CacheStats struct {
	ResolvedRefHits   int64
	ResolvedRefMisses int64
	MethodSetHits     int64
	MethodSetMisses   int64
	PackageHits       int64
	PackageMisses     int64
	IdentifierTypeHits int64     // NEW
	IdentifierTypeMisses int64   // NEW
	MethodTypeMapHits   int64    // NEW
	MethodTypeMapMisses int64    // NEW
	LastReset         time.Time
}

// NewSymbolCache creates a new symbol cache
func NewSymbolCache() *SymbolCache {
	return &SymbolCache{
		resolvedRefs:    make(map[string]*types.Symbol),
		methodSets:      make(map[string][]*types.Symbol),
		packageSymbols:  make(map[string]*types.SymbolTable),
		identifierTypes: make(map[string]*types.Symbol), // NEW
		methodTypeMap:   make(map[string]bool),          // NEW
		cacheStats: CacheStats{
			LastReset: time.Now(),
		},
	}
}

// Resolved reference caching

// GetResolvedRef retrieves a cached resolved symbol reference
func (c *SymbolCache) GetResolvedRef(key string) *types.Symbol {
	c.refsLock.RLock()
	defer c.refsLock.RUnlock()

	if symbol, exists := c.resolvedRefs[key]; exists {
		c.incrementResolvedRefHits()
		return symbol
	}

	c.incrementResolvedRefMisses()
	return nil
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

	if methods, exists := c.methodSets[key]; exists {
		c.incrementMethodSetHits()
		return methods
	}

	c.incrementMethodSetMisses()
	return nil
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

	if symbol, exists := c.identifierTypes[key]; exists {
		c.incrementIdentifierTypeHits()
		return symbol
	}

	c.incrementIdentifierTypeMisses()
	return nil
}

// SetIdentifierType caches an identifier type resolution
func (c *SymbolCache) SetIdentifierType(key string, symbol *types.Symbol) {
	c.typesLock.Lock()
	defer c.typesLock.Unlock()

	c.identifierTypes[key] = symbol
}

// Method-to-type mapping caching (NEW)

// GetMethodTypeMapping retrieves a cached method-to-type mapping
func (c *SymbolCache) GetMethodTypeMapping(key string) (bool, bool) {
	c.methodTypeMapLock.RLock()
	defer c.methodTypeMapLock.RUnlock()

	if result, exists := c.methodTypeMap[key]; exists {
		c.incrementMethodTypeMapHits()
		return result, true
	}

	c.incrementMethodTypeMapMisses()
	return false, false
}

// SetMethodTypeMapping caches a method-to-type mapping
func (c *SymbolCache) SetMethodTypeMapping(key string, matches bool) {
	c.methodTypeMapLock.Lock()
	defer c.methodTypeMapLock.Unlock()

	c.methodTypeMap[key] = matches
}

// Package symbol table caching

// GetPackageSymbols retrieves a cached package symbol table
func (c *SymbolCache) GetPackageSymbols(packagePath string) *types.SymbolTable {
	c.packagesLock.RLock()
	defer c.packagesLock.RUnlock()

	if symbolTable, exists := c.packageSymbols[packagePath]; exists {
		c.incrementPackageHits()
		return symbolTable
	}

	c.incrementPackageMisses()
	return nil
}

// SetPackageSymbols caches a package symbol table
func (c *SymbolCache) SetPackageSymbols(packagePath string, symbolTable *types.SymbolTable) {
	c.packagesLock.Lock()
	defer c.packagesLock.Unlock()

	c.packageSymbols[packagePath] = symbolTable
}

// Cache invalidation

// InvalidatePackage removes all cache entries related to a package
func (c *SymbolCache) InvalidatePackage(packagePath string) {
	// Remove package symbol table
	c.packagesLock.Lock()
	delete(c.packageSymbols, packagePath)
	c.packagesLock.Unlock()

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

// Clear removes all cache entries
func (c *SymbolCache) Clear() {
	c.refsLock.Lock()
	c.resolvedRefs = make(map[string]*types.Symbol)
	c.refsLock.Unlock()

	c.methodsLock.Lock()
	c.methodSets = make(map[string][]*types.Symbol)
	c.methodsLock.Unlock()

	c.packagesLock.Lock()
	c.packageSymbols = make(map[string]*types.SymbolTable)
	c.packagesLock.Unlock()

	// NEW: Clear identifier types cache
	c.typesLock.Lock()
	c.identifierTypes = make(map[string]*types.Symbol)
	c.typesLock.Unlock()

	// NEW: Clear method-to-type mapping cache
	c.methodTypeMapLock.Lock()
	c.methodTypeMap = make(map[string]bool)
	c.methodTypeMapLock.Unlock()

	c.resetStats()
}

// Cache statistics

// GetStats returns current cache statistics
func (c *SymbolCache) GetStats() CacheStats {
	c.statsLock.RLock()
	defer c.statsLock.RUnlock()

	return c.cacheStats
}

// ResetStats resets cache statistics
func (c *SymbolCache) ResetStats() {
	c.resetStats()
}

// GetCacheSize returns the current size of all caches
func (c *SymbolCache) GetCacheSize() CacheSize {
	c.refsLock.RLock()
	resolvedRefsCount := len(c.resolvedRefs)
	c.refsLock.RUnlock()

	c.methodsLock.RLock()
	methodSetsCount := len(c.methodSets)
	c.methodsLock.RUnlock()

	c.packagesLock.RLock()
	packageSymbolsCount := len(c.packageSymbols)
	c.packagesLock.RUnlock()

	return CacheSize{
		ResolvedRefs:   resolvedRefsCount,
		MethodSets:     methodSetsCount,
		PackageSymbols: packageSymbolsCount,
	}
}

// CacheSize represents the current size of caches
type CacheSize struct {
	ResolvedRefs   int
	MethodSets     int
	PackageSymbols int
}

// Cache efficiency methods

// GetHitRate returns the overall cache hit rate as a percentage
func (c *SymbolCache) GetHitRate() float64 {
	stats := c.GetStats()
	
	totalHits := stats.ResolvedRefHits + stats.MethodSetHits + stats.PackageHits
	totalRequests := totalHits + stats.ResolvedRefMisses + stats.MethodSetMisses + stats.PackageMisses
	
	if totalRequests == 0 {
		return 0.0
	}
	
	return float64(totalHits) / float64(totalRequests) * 100.0
}

// GetResolvedRefHitRate returns the hit rate for resolved references
func (c *SymbolCache) GetResolvedRefHitRate() float64 {
	stats := c.GetStats()
	total := stats.ResolvedRefHits + stats.ResolvedRefMisses
	
	if total == 0 {
		return 0.0
	}
	
	return float64(stats.ResolvedRefHits) / float64(total) * 100.0
}

// GetMethodSetHitRate returns the hit rate for method sets
func (c *SymbolCache) GetMethodSetHitRate() float64 {
	stats := c.GetStats()
	total := stats.MethodSetHits + stats.MethodSetMisses
	
	if total == 0 {
		return 0.0
	}
	
	return float64(stats.MethodSetHits) / float64(total) * 100.0
}

// GetPackageHitRate returns the hit rate for package symbols
func (c *SymbolCache) GetPackageHitRate() float64 {
	stats := c.GetStats()
	total := stats.PackageHits + stats.PackageMisses
	
	if total == 0 {
		return 0.0
	}
	
	return float64(stats.PackageHits) / float64(total) * 100.0
}

// Private helper methods for statistics

func (c *SymbolCache) incrementResolvedRefHits() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.ResolvedRefHits++
}

func (c *SymbolCache) incrementResolvedRefMisses() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.ResolvedRefMisses++
}

func (c *SymbolCache) incrementMethodSetHits() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.MethodSetHits++
}

func (c *SymbolCache) incrementMethodSetMisses() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.MethodSetMisses++
}

func (c *SymbolCache) incrementPackageHits() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.PackageHits++
}

func (c *SymbolCache) incrementPackageMisses() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.PackageMisses++
}

func (c *SymbolCache) incrementIdentifierTypeHits() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.IdentifierTypeHits++
}

func (c *SymbolCache) incrementIdentifierTypeMisses() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.IdentifierTypeMisses++
}

func (c *SymbolCache) incrementMethodTypeMapHits() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.MethodTypeMapHits++
}

func (c *SymbolCache) incrementMethodTypeMapMisses() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()
	c.cacheStats.MethodTypeMapMisses++
}

func (c *SymbolCache) resetStats() {
	c.statsLock.Lock()
	defer c.statsLock.Unlock()

	c.cacheStats = CacheStats{
		LastReset: time.Now(),
	}
}

// Cache warming methods

// WarmCache pre-populates the cache with commonly accessed symbols
func (c *SymbolCache) WarmCache(workspace *types.Workspace) {
	// Cache all package symbol tables
	for packagePath, pkg := range workspace.Packages {
		if pkg.Symbols != nil {
			c.SetPackageSymbols(packagePath, pkg.Symbols)
		}
	}
}

// Maintenance methods

// CleanupStaleEntries removes old cache entries (not implemented in this basic version)
// In a production system, this could remove entries based on last access time
func (c *SymbolCache) CleanupStaleEntries() {
	// This is a placeholder for more sophisticated cache management
	// Could be implemented with TTL (time-to-live) tracking
}

// GetMemoryUsage estimates memory usage (basic implementation)
func (c *SymbolCache) GetMemoryUsage() int64 {
	// This is a rough estimate - in production, you'd want more accurate measurement
	size := c.GetCacheSize()
	
	// Rough estimate: each symbol ~1KB, each method set ~5KB average
	estimatedBytes := int64(size.ResolvedRefs*1024 + size.MethodSets*5120 + size.PackageSymbols*10240)
	
	return estimatedBytes
}