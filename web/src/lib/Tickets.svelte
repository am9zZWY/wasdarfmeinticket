<script>
  // The list of tickets the user holds, with a form to add one.
  let { tickets = $bindable([]), laendertickets = [], labelOf } = $props();

  let type = $state('deutschlandticket');
  let laenderticket = $state('');
  let net = $state('');
  let scope = $state('network'); // of a Verbund ticket: network, zones or route
  let zones = $state('');
  let from = $state('');
  let to = $state('');

  let draft = $derived.by(() => {
    if (type === 'deutschlandticket') {
      return { type };
    }
    if (type === 'laenderticket') {
      return { type, name: laenderticket || laendertickets[0]?.id };
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
    zones = from = to = '';
  }

  function remove(position) {
    tickets = tickets.filter((_, index) => index !== position);
  }
</script>

<section>
  <h2>Meine Tickets</h2>

  {#if tickets.length === 0}
    <p class="hint">Noch kein Ticket. Füge unten hinzu, was du hast.</p>
  {:else}
    <ul class="tickets">
      {#each tickets as ticket, position}
        <li>
          <span>{labelOf(ticket)}</span>
          <button type="button" class="quiet" onclick={() => remove(position)} aria-label="{labelOf(ticket)} entfernen">Entfernen</button>
        </li>
      {/each}
    </ul>
  {/if}

  <form class="card" onsubmit={add}>
    <label>
      Art
      <select bind:value={type}>
        <option value="deutschlandticket">Deutschlandticket</option>
        <option value="laenderticket">Länderticket</option>
        <option value="verbund">Verbundticket</option>
      </select>
    </label>

    {#if type === 'laenderticket'}
      <label>
        Welches
        <select bind:value={laenderticket}>
          {#each laendertickets as option}
            <option value={option.id}>{option.label}</option>
          {/each}
        </select>
      </label>
    {:else if type === 'verbund'}
      <label>
        Verbund
        <input bind:value={net} placeholder="z. B. naldo" autocapitalize="none" />
      </label>
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
        <label>
          Von
          <input bind:value={from} placeholder="Haltestelle" />
        </label>
        <label>
          Nach
          <input bind:value={to} placeholder="Haltestelle" />
        </label>
        <p class="hint">Die Zonen der Strecke werden beim Prüfen ermittelt. Der Verbund darf dann leer bleiben.</p>
      {/if}
    {/if}

    {#if missing}
      <p class="hint">{missing}</p>
    {/if}
    <button type="submit" disabled={Boolean(missing)}>Ticket hinzufügen</button>
  </form>
</section>
