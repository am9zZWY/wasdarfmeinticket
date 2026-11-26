<script>
  import { onMount } from 'svelte';
  import { loadEngine } from './lib/engine.js';
  import Result from './lib/Result.svelte';
  import Suggest from './lib/Suggest.svelte';
  import { loadTickets, saveTickets } from './lib/storage.js';
  import Tickets from './lib/Tickets.svelte';

  let engine = $state(null);
  let engineError = $state('');

  let tickets = $state(loadTickets());
  let link = $state(sharedText());
  let mode = $state(link ? 'link' : 'stops'); // how the connection is given: stops or link
  let pastedAnswer = $state('');
  let from = $state('');
  let to = $state('');
  let departure = $state(nextFiveMinutes());

  let running = $state(false);
  let progress = $state([]);
  let outcome = $state(null);
  let failure = $state('');

  onMount(async () => {
    try {
      engine = await loadEngine();
    } catch (error) {
      engineError = String(error?.message ?? error);
    }
  });

  $effect(() => saveTickets(tickets));

  // What the pasted text is, as soon as the engine can tell.
  let linkInfo = $derived(engine && link.trim() ? engine.describeLink(link) : { kind: 'empty' });

  // The request for the engine, or the reason why it cannot be sent yet.
  let plan = $derived.by(() => {
    if (tickets.length === 0) {
      return { problem: 'Füge mindestens ein Ticket hinzu.' };
    }
    if (mode === 'stops') {
      if (!from.trim() || !to.trim() || !departure) {
        return { problem: 'Gib Start, Ziel und Abfahrtszeit an.' };
      }
      return { request: { from: from.trim(), to: to.trim(), departure, tickets } };
    }
    if (linkInfo.kind === 'empty') {
      return { problem: 'Füge einen Link von bahn.de ein.' };
    }
    if (linkInfo.kind === 'none') {
      return { problem: 'Darin ist kein Link von bahn.de zu erkennen.' };
    }
    if (linkInfo.kind === 'share') {
      if (!pastedAnswer.trim()) {
        return { problem: 'Füge die Antwort von bahn.de ein (siehe oben).' };
      }
      return { request: { recon: pastedAnswer, tickets } };
    }
    return { request: { link, tickets } };
  });

  async function check(event) {
    event.preventDefault();
    if (!engine || !plan.request || running) {
      return;
    }
    running = true;
    progress = [];
    outcome = null;
    failure = '';
    try {
      outcome = await engine.check($state.snapshot(plan.request), (message) => (progress = [...progress, message]));
    } catch (error) {
      failure = String(error?.message ?? error);
    } finally {
      running = false;
    }
  }

  // Stop names for the suggestions under the stop fields. Transitous asks
  // its users to go easy on it: only from 3 letters on, and only once the
  // typing pauses.
  async function suggestStops(text) {
    return engine ? (await engine.suggestStops(text)).map((name) => ({ value: name })) : [];
  }

  function swap() {
    [from, to] = [to, from];
  }

  // When the app is opened through the system's share menu, the shared
  // link arrives in the page address.
  function sharedText() {
    const shared = new URLSearchParams(location.search);
    const text = ['url', 'text', 'title'].map((name) => shared.get(name)).filter(Boolean).join(' ');
    if (text) {
      history.replaceState(null, '', location.pathname);
    }
    return text;
  }

  // The current time, rounded up to five minutes, as the input field wants it.
  function nextFiveMinutes() {
    const fiveMinutes = 5 * 60 * 1000;
    const time = new Date(Math.ceil(Date.now() / fiveMinutes) * fiveMinutes);
    const pad = (number) => String(number).padStart(2, '0');
    return `${time.getFullYear()}-${pad(time.getMonth() + 1)}-${pad(time.getDate())}T${pad(time.getHours())}:${pad(time.getMinutes())}`;
  }
</script>

<main>
  {#if outcome}
    <button type="button" class="quiet" onclick={() => (outcome = null)}>
      <svg width="18" height="18" viewBox="0 0 18 18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M11 4l-5 5 5 5" /></svg>
      Ändern
    </button>
    <Result view={outcome.view} markdown={outcome.markdown} />
    <button type="button" class="secondary" onclick={() => (outcome = null)}>Andere Verbindung prüfen</button>
  {:else}
    <header>
      <h1>Was darf mein Ticket?</h1>
      <p>Gib eine Verbindung ein und sieh, wo deine Tickets gelten.</p>
    </header>

    {#if engineError}
      <p class="verdict invalid" role="alert">Die App konnte nicht starten: {engineError}</p>
    {/if}

    <Tickets bind:tickets laendertickets={engine?.laendertickets ?? []} verbuende={engine?.verbuende ?? []} {suggestStops} labelOf={(ticket) => engine?.ticketLabel(ticket) || ticket.type} />

    <form class="card" onsubmit={check} id="connection">
      <div class="card-head">
        <h2>Verbindung</h2>
        <div class="modes" role="group" aria-label="Verbindung angeben über">
          <button type="button" class:active={mode === 'stops'} aria-pressed={mode === 'stops'} onclick={() => (mode = 'stops')}>Strecke</button>
          <button type="button" class:active={mode === 'link'} aria-pressed={mode === 'link'} onclick={() => (mode = 'link')}>Link</button>
        </div>
      </div>

      {#if mode === 'link'}
        <label>
          Link von bahn.de oder aus dem DB Navigator
          <textarea bind:value={link} rows="3" placeholder="https://www.bahn.de/buchung/…"></textarea>
        </label>
        {#if linkInfo.kind === 'share'}
          <p class="hint">
            Das ist ein geteilter Link. Die Bahn lässt Webseiten ihn nicht nachschlagen, dein Browser darf es aber:
          </p>
          <ol class="hint">
            <li><a href={linkInfo.lookupURL} target="_blank" rel="noopener">Antwort von bahn.de öffnen</a></li>
            <li>Dort den ganzen Text kopieren.</li>
            <li>Hier einfügen:</li>
          </ol>
          <label>
            Antwort von bahn.de
            <textarea bind:value={pastedAnswer} rows="3" placeholder={'{"hinfahrtRecon": …'}></textarea>
          </label>
          <p class="hint">
            Ohne diesen Umweg geht es mit der Adresse, die bahn.de bei geöffneter Verbindung in der Adresszeile zeigt.
          </p>
        {:else if linkInfo.kind === 'search'}
          <p class="hint">Suchlink: Es wird die erste Verbindung ab der gesuchten Zeit genommen.</p>
        {:else}
          <p class="hint">Im Navigator: Verbindung öffnen › Teilen › Link kopieren.</p>
        {/if}
      {:else}
        <div class="fields">
          <div class="stops">
            <div class="fields">
              <Suggest label="Von" bind:value={from} suggest={suggestStops} delay={250} minLength={3} placeholder="z. B. Tübingen Hbf" />
              <Suggest label="Nach" bind:value={to} suggest={suggestStops} delay={250} minLength={3} placeholder="z. B. Heidelberg Hbf" />
            </div>
            <button type="button" class="round" onclick={swap} aria-label="Start und Ziel tauschen">
              <svg width="18" height="18" viewBox="0 0 18 18" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 3v12M3 6l3-3 3 3M12 15V3M9 12l3 3 3-3" /></svg>
            </button>
          </div>
          <label>
            Abfahrt
            <input type="datetime-local" bind:value={departure} />
          </label>
        </div>
      {/if}
    </form>

    <button type="submit" form="connection" class="primary" disabled={!engine || running || Boolean(plan.problem)}>
      {#if !engine && !engineError}
        Wird geladen …
      {:else if running}
        Prüfe …
      {:else}
        Prüfen
      {/if}
    </button>
    {#if engine && plan.problem}
      <p class="hint">{plan.problem}</p>
    {/if}

    {#if running || (failure && progress.length > 0)}
      <ul class="progress" aria-live="polite">
        {#each progress as message}
          <li>{message}</li>
        {/each}
      </ul>
    {/if}

    {#if failure}
      <p class="verdict invalid" role="alert">{failure}</p>
    {/if}
  {/if}

  <footer>
    <p>
      Fahrplandaten: <a href="https://transitous.org/sources/" target="_blank" rel="noopener">Transitous und seine Quellen</a>. Haltestellen und
      Karten: © <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap-Mitwirkende</a>. Zonen und Tarife:
      Auskünfte der Verkehrsverbünde. Alle Angaben ohne Gewähr.
    </p>
    <p>Deine Tickets bleiben auf diesem Gerät. Anfragen gehen direkt von hier an die Auskünfte.</p>
    <p>
      Kontakt, Fehler melden und Quellcode:
      <a href="https://github.com/am9zZWY/wasdarfmeinticket" target="_blank" rel="noopener">github.com/am9zZWY/wasdarfmeinticket</a>
    </p>
  </footer>
</main>
