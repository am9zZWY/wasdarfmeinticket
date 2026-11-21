// The ticket check itself is Go code compiled to WebAssembly. This module
// loads it once and offers its functions to the user interface.
import '../generated/wasm_exec.js';

let loading;

// loadEngine starts the engine on first use and returns it.
export function loadEngine() {
  loading ??= start();
  return loading;
}

async function start() {
  const go = new globalThis.Go();
  const address = new URL('engine.wasm', document.baseURI);
  let source;
  try {
    source = await WebAssembly.instantiateStreaming(fetch(address), go.importObject);
  } catch {
    // Hosts that do not label the file as WebAssembly need the slower way.
    const bytes = await (await fetch(address)).arrayBuffer();
    source = await WebAssembly.instantiate(bytes, go.importObject);
  }
  go.run(source.instance); // keeps running; the engine registers itself
  const engine = globalThis.wasdarfmeinticket;
  return {
    // check runs a check; onProgress receives each progress message.
    // It resolves with { view, markdown }.
    check: async (request, onProgress) => JSON.parse(await engine.check(JSON.stringify(request), onProgress)),
    // describeLink says what a pasted text is: { kind, lookupURL?, error? }
    // with kind "share", "connection", "search" or "none".
    describeLink: (text) => JSON.parse(engine.describeLink(text)),
    ticketLabel: (ticket) => engine.ticketLabel(JSON.stringify(ticket)),
    laendertickets: JSON.parse(engine.laendertickets),
  };
}
