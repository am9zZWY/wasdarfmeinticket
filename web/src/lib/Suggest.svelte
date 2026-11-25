<script>
  // A text field that offers suggestions below it while typing (an ARIA
  // combobox). suggest(text) returns, or resolves to, the items to show:
  // { value, hint? }. Typed text that matches no item is kept as it is.
  let { label, value = $bindable(''), suggest, delay = 0, minLength = 0, ...input } = $props();

  const id = $props.id();
  let items = $state([]);
  let open = $state(false);
  let active = $state(-1);
  let timer;
  let asked = 0; // number of the latest lookup; older answers are dropped

  function lookup() {
    clearTimeout(timer);
    const ask = ++asked;
    const text = value.trim();
    if (text.length < minLength) {
      items = [];
      return;
    }
    timer = setTimeout(async () => {
      let found;
      try {
        found = await suggest(text);
      } catch {
        found = []; // no suggestions is no reason to bother the user
      }
      if (ask === asked) {
        items = found;
        active = -1;
      }
    }, delay);
  }

  function choose(item) {
    value = item.value;
    open = false;
    items = [];
  }

  function keydown(event) {
    const shown = open ? items.length : 0;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      if (!open) {
        open = true;
        lookup();
      } else if (shown) {
        active = event.key === 'ArrowDown' ? (active + 1) % shown : active <= 0 ? shown - 1 : active - 1;
      }
    } else if (event.key === 'Enter' && shown && active >= 0) {
      event.preventDefault();
      choose(items[active]);
    } else if (event.key === 'Escape' && shown) {
      event.preventDefault();
      open = false;
    }
  }

  let expanded = $derived(open && items.length > 0);
</script>

<div class="suggest">
  <label for={id}>{label}</label>
  <input
    {id}
    {...input}
    bind:value
    role="combobox"
    aria-expanded={expanded}
    aria-controls="{id}-list"
    aria-autocomplete="list"
    aria-activedescendant={expanded && active >= 0 ? `${id}-${active}` : undefined}
    autocomplete="off"
    oninput={() => {
      open = true;
      lookup();
    }}
    onfocus={() => {
      open = true;
      lookup();
    }}
    onblur={() => (open = false)}
    onkeydown={keydown}
  />
  {#if expanded}
    <ul role="listbox" id="{id}-list" aria-label={label}>
      {#each items as item, position}
        <li
          role="option"
          id="{id}-{position}"
          tabindex="-1"
          aria-selected={position === active}
          class:active={position === active}
          onmousedown={(event) => {
            event.preventDefault(); // keep the focus in the field
            choose(item);
          }}
        >
          <span>{item.value}</span>
          {#if item.hint}
            <span class="hint">{item.hint}</span>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>
