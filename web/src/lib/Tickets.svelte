<script>
  // The tickets the user holds, as chips, with a picker to add one.
  import Suggest from './Suggest.svelte';

  let { tickets = $bindable([]), laendertickets = [], verbuende = [], suggestStops, labelOf } = $props();

  // The picker opens by itself as long as there is no ticket.
  let adding = $state(tickets.length === 0);

  // Everything the search offers. Typed text that matches none of them is
  // taken as the abbreviation of a Verbund that is not listed.
  let options = $derived([
    { label: 'Deutschlandticket', kind: 'bundesweit', ticket: { type: 'deutschlandticket' } },
    ...laendertickets.map((option) => ({ label: option.label, kind: 'Bundesland', ticket: { type: 'laenderticket', name: option.id } })),
    ...verbuende.map((option) => ({ label: option.label, kind: 'Verkehrsverbund', net: option.id, ticket: { type: 'verbund' } })),
  ].sort((a, b) => a.label.localeCompare(b.label, 'de', { sensitivity: 'base' })));

  let query = $state('');

  // The options containing the typed text, in name or abbreviation.
  function suggestTickets(text) {
    const typed = text.toLowerCase();
    return options
      .filter((option) => option.label.toLowerCase().includes(typed) || option.net?.includes(typed))
      .map((option) => ({ value: option.label, hint: option.kind }));
  }
  let match = $derived.by(() => {
    const text = query.trim().toLowerCase();
    return text ? options.find((option) => option.label.toLowerCase() === text || option.net === text) : undefined;
  });
  let type = $derived(match ? match.ticket.type : query.trim() ? 'verbund' : '');
  let net = $derived(match ? (match.net ?? '') : query.trim());

  let scope = $state('network'); // of a Verbund ticket: network, zones or route
  let zones = $state('');
  let from = $state('');
  let to = $state('');

  let draft = $derived.by(() => {
    if (type !== 'verbund') {
      return { ...match?.ticket };
    }
    const verbund = { type: 'verbund' };
    if (net.trim()) {
      verbund.net = net.trim();
    }
    if (scope === 'zones') {
      verbund.zones = zones.split(/[,;\s]+/).filter(Boolean);
    } else if (scope === 'route') {
      verbund.from = from.trim();
      verbund.to = to.trim();
    } else {
      verbund.network = true;
    }
    return verbund;
  });

  // What is still missing before the draft can be added, or "".
  let missing = $derived.by(() => {
    if (!type) {
      return 'Such dein Ticket aus der Liste aus.';
    }
    if (draft.type !== 'verbund') {
      return '';
    }
    if (scope === 'route') {
      return draft.from && draft.to ? '' : 'Start und Ziel der Strecke fehlen.';
    }
    if (!draft.net) {
      return 'Der Verbund fehlt.';
    }
    return scope === 'zones' && draft.zones.length === 0 ? 'Die Zonen fehlen.' : '';
  });

  function add(event) {
    event.preventDefault();
    if (missing) {
      return;
    }
    tickets = [...tickets, draft];
    query = zones = from = to = '';
    adding = false;
  }

  function remove(position) {
    tickets = tickets.filter((_, index) => index !== position);
  }
</script>

<section class="card">
  <div class="card-head">
    <h2>Meine Tickets</h2>
    <span>nur auf diesem Gerät</span>
  </div>

  {#if tickets.length > 0 || !adding}
    <ul class="chips">
      {#each tickets as ticket, position}
        <li>
          <span>{labelOf(ticket)}</span>
          <button type="button" class="remove" onclick={() => remove(position)} aria-label="{labelOf(ticket)} entfernen">
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M3.5 3.5l7 7M10.5 3.5l-7 7" /></svg>
          </button>
        </li>
      {/each}
      {#if !adding}
        <li class="chip-add"><button type="button" class="add-ticket" onclick={() => (adding = true)}>+ Ticket</button></li>
      {/if}
    </ul>
  {/if}

  {#if tickets.length === 0 && !adding}
    <p class="hint">Noch kein Ticket. Füge hinzu, was du hast.</p>
  {/if}

  {#if adding}
    <form class="picker" onsubmit={add}>
      <div class="picker-row">
        <Suggest label="Welches Ticket hast du?" bind:value={query} suggest={suggestTickets} type="search" placeholder="Suchen, z. B. Bayern oder naldo" autocapitalize="none" />
        <button type="submit" class="dark" disabled={Boolean(missing)}>Hinzufügen</button>
      </div>
      {#if query.trim() && !match}
        <p class="hint">Nicht in der Liste: „{query.trim()}“ wird als Kürzel eines Verkehrsverbunds geprüft.</p>
      {/if}
      {#if type === 'verbund'}
        <label>
          Gilt für
          <select bind:value={scope}>
            <option value="network">das ganze Netz</option>
            <option value="zones">bestimmte Zonen</option>
            <option value="route">eine Strecke</option>
          </select>
        </label>
        {#if scope === 'zones'}
          <label>
            Zonen
            <input bind:value={zones} placeholder="z. B. 11, 220" inputmode="text" />
          </label>
        {:else if scope === 'route'}
          <Suggest label="Von" bind:value={from} suggest={suggestStops} delay={250} minLength={3} placeholder="Haltestelle" />
          <Suggest label="Nach" bind:value={to} suggest={suggestStops} delay={250} minLength={3} placeholder="Haltestelle" />
          <p class="hint">Die Zonen der Strecke werden beim Prüfen ermittelt. Der Verbund darf dann leer bleiben.</p>
        {/if}
      {/if}

      {#if missing}
        <p class="hint">{missing}</p>
      {/if}
    </form>
  {/if}
</section>
