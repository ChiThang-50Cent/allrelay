#!/bin/bash
# AllRelay Phase 2 — Build & Test
# Builds Java server + Go server + C client, then runs all tests.
#
# Usage:
#   ./scripts/setup-env.sh      # first: check + set up env
#   source scripts/setup-env.sh # apply env vars
#   ./scripts/build-phase2.sh   # build + test
#   ./scripts/build-phase2.sh --test-only  # skip build, just test

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

PASS=0; FAIL=0
pass() { echo -e "  ${GREEN}✅ $1${NC}"; ((PASS++)); }
fail() { echo -e "  ${RED}❌ $1${NC}"; ((FAIL++)); }
warn() { echo -e "  ${YELLOW}⚠️  $1${NC}"; }
info() { echo -e "${BLUE}[*]${NC} $1"; }
header() { echo -e "\n${GREEN}━━━ $1 ━━━${NC}"; }

TEST_ONLY=false
[[ "$1" == "--test-only" ]] && TEST_ONLY=true

# ─── Env check ───────────────────────────────────────────────

[ -z "$ANDROID_SDK_ROOT" ] && export ANDROID_SDK_ROOT="$HOME/android-sdk"
if [ ! -d "$ANDROID_SDK_ROOT" ]; then
    echo -e "${RED}ANDROID_SDK_ROOT=$ANDROID_SDK_ROOT not found.${NC}"
    echo "Run: source scripts/setup-env.sh"
    exit 1
fi

# ─── 1. Java Server ─────────────────────────────────────────

header "1. Java Server"

if ! $TEST_ONLY; then
    cd "$PROJECT_ROOT/scrcpy"
    info "Compiling..."
    ./gradlew :server:compileReleaseJavaWithJavac --no-daemon -q 2>&1
    pass "Compiled"

    info "Assembling APK..."
    ./gradlew :server:assembleRelease --no-daemon -q 2>&1
    APK=$(find server/build/outputs/apk -name "*.apk" 2>/dev/null | head -1)
    if [ -f "$APK" ]; then
        mkdir -p "$PROJECT_ROOT/bin"
        cp "$APK" "$PROJECT_ROOT/bin/scrcpy-server-allrelay"
        pass "Built: bin/scrcpy-server-allrelay ($(du -h "$APK" | cut -f1))"
    else
        fail "APK not found"
    fi
else
    info "Skipping build (--test-only)"
fi

# ─── 2. Go Server ───────────────────────────────────────────

header "2. Go Server"

cd "$PROJECT_ROOT/allrelay-server"

if ! $TEST_ONLY; then
    info "Building..."
    go build -o bin/allrelay-server ./cmd/allrelay-server/ 2>&1
    cp bin/allrelay-server "$PROJECT_ROOT/bin/allrelay-server"
    pass "Built: bin/allrelay-server ($(du -h bin/allrelay-server | cut -f1))"
fi

info "go vet..."
if go vet ./... 2>&1; then
    pass "Clean"
else
    fail "Issues found"
fi

info "Running tests..."
if go test ./... -count=1 -timeout 10s 2>&1; then
    pass "All Go tests pass"
else
    fail "Go tests failed"
    cat /tmp/allrelay-test.log
fi

# ─── 3. C Client ────────────────────────────────────────────

header "3. C Client"

if ! $TEST_ONLY; then
    if pkg-config --exists sdl3 2>/dev/null; then
        cd "$PROJECT_ROOT/scrcpy"
        info "Configuring meson..."
        meson setup build-phase2 \
            -Dcompile_app=true \
            -Dcompile_server=false \
            -Dprebuilt_server="$PROJECT_ROOT/bin/scrcpy-server-allrelay" \
            --wipe -q 2>&1

        info "Compiling..."
        ninja -C build-phase2 -j$(nproc) 2>&1

        if [ -f build-phase2/app/scrcpy ]; then
            cp build-phase2/app/scrcpy "$PROJECT_ROOT/bin/scrcpy"
            pass "Built: bin/scrcpy ($(du -h bin/scrcpy | cut -f1))"
        else
            fail "Binary not found"
        fi
    else
        warn "SDL3 missing — skipping C client"
        echo "  Run: ./scripts/setup-env.sh for install instructions"
    fi
else
    info "Skipping build (--test-only)"
fi

# ─── Summary ─────────────────────────────────────────────────

header "Results"
echo ""
echo -e "  ${GREEN}Passed: $PASS${NC}  ${RED}Failed: $FAIL${NC}"
echo ""
echo "Artifacts:"
ls -lh "$PROJECT_ROOT/bin/" 2>/dev/null || echo "  (none)"
echo ""

[ "$FAIL" -eq 0 ] && echo -e "${GREEN}All OK ✅${NC}" || echo -e "${RED}Some checks failed ❌${NC}"
exit $FAIL
