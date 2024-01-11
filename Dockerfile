FROM scratch
COPY custom-errors.wasm ./plugin.wasm
ENTRYPOINT [ "plugin.wasm" ]
