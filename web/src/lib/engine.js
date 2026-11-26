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
  // ponytail: suggestions are kept for the session only, unbounded; a
  // session types few enough texts for that.
  const suggestions = new Map();
  return {
    // check runs a check; onProgress receives each progress message, and
    // aborting signal (an AbortSignal) stops it. It resolves with
    // { view, markdown }.
    check: async (request, onProgress, signal) => JSON.parse(await engine.check(JSON.stringify(request), onProgress, signal)),
    // describeLink says what a pasted text is: { kind, lookupURL?, error? }
    // with kind "share", "connection", "search" or "none".
    // suggestStops resolves with the names of the stops matching a partly
    // typed text, best first. Repeated texts are answered from memory.
    suggestStops: (text) => {
      const key = text.toLowerCase();
      if (!suggestions.has(key)) {
        const lookup = engine.suggestStops(text).then(JSON.parse);
        lookup.catch(() => suggestions.delete(key)); // ask again next time
        suggestions.set(key, lookup);
      }
      return suggestions.get(key);
    },
    describeLink: (text) => JSON.parse(engine.describeLink(text)),
    ticketLabel: (ticket) => engine.ticketLabel(JSON.stringify(ticket)),
    laendertickets: JSON.parse(engine.laendertickets),
    verbuende: JSON.parse(engine.verbuende),
  };
}
