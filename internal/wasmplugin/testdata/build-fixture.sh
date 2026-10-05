#!/bin/sh
set -eu
cd "$(dirname "$0")"
rustc --edition 2021 --target wasm32-unknown-unknown --crate-type cdylib -C opt-level=s -C panic=abort -C strip=debuginfo fixture.rs -o fixture.wasm
