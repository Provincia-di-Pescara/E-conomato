(function () {
  'use strict';

  var CATALOG_URL = '/static/js/fa-icons.json';
  var ALIASES_URL = '/static/js/icon-aliases.json';
  var MAX_RESULTS = 240;
  var DEFAULT_FAMILY = 'fa-solid';

  var loadPromise = null;

  function loadCatalog() {
    if (loadPromise) return loadPromise;
    loadPromise = Promise.all([
      fetch(CATALOG_URL).then(function (r) { return r.json(); }),
      fetch(ALIASES_URL).then(function (r) { return r.json(); }).catch(function () { return {}; })
    ]).then(function (parts) {
      var names = parts[0];
      var aliases = parts[1] || {};
      // Build reverse index: for each fa-name, collect Italian aliases that map to it.
      var revIdx = {};
      Object.keys(aliases).forEach(function (it) {
        var arr = aliases[it] || [];
        arr.forEach(function (faName) {
          if (!revIdx[faName]) revIdx[faName] = [];
          revIdx[faName].push(it);
        });
      });
      var items = names.map(function (name) {
        var its = revIdx[name] || [];
        return {
          name: name,
          searchText: (name + ' ' + its.join(' ')).toLowerCase()
        };
      });
      return items;
    });
    return loadPromise;
  }

  function parseClass(value) {
    var v = (value || '').trim();
    if (!v) return { family: DEFAULT_FAMILY, name: '' };
    var parts = v.split(/\s+/);
    var family = DEFAULT_FAMILY;
    var name = '';
    parts.forEach(function (p) {
      if (p === 'fa-solid' || p === 'fa-regular' || p === 'fa-brands' || p === 'fas' || p === 'far' || p === 'fab') {
        family = p === 'fas' ? 'fa-solid' : p === 'far' ? 'fa-regular' : p === 'fab' ? 'fa-brands' : p;
      } else if (p.indexOf('fa-') === 0) {
        name = p.substring(3);
      }
    });
    return { family: family, name: name };
  }

  function buildClass(family, name) {
    if (!name) return '';
    return family + ' fa-' + name;
  }

  function renderGrid(grid, items, query, current) {
    var q = (query || '').toLowerCase().trim();
    var matched = [];
    if (q === '') {
      matched = items.slice(0, MAX_RESULTS);
    } else {
      for (var i = 0; i < items.length && matched.length < MAX_RESULTS; i++) {
        if (items[i].searchText.indexOf(q) !== -1) matched.push(items[i]);
      }
    }
    var html = '';
    if (matched.length === 0) {
      html = '<div class="ec-icon-picker__empty">Nessuna icona corrisponde a &laquo;' + escapeHtml(q) + '&raquo;.</div>';
    } else {
      var noneSelected = current.name === '';
      html += '<button type="button" class="ec-icon-picker__cell' + (noneSelected ? ' is-selected' : '') + '" data-icona="" title="Nessuna icona"><span style="font-size:11px;color:var(--ec-ink-500)">—</span></button>';
      for (var j = 0; j < matched.length; j++) {
        var it = matched[j];
        var cls = buildClass(DEFAULT_FAMILY, it.name);
        var sel = (it.name === current.name) ? ' is-selected' : '';
        html += '<button type="button" class="ec-icon-picker__cell' + sel + '" data-icona="' + cls + '" title="' + it.name + '"><i class="' + cls + '"></i></button>';
      }
      if (q === '' && items.length > MAX_RESULTS) {
        html += '<div class="ec-icon-picker__hint">Mostro prime ' + MAX_RESULTS + ' icone — usa la ricerca per filtrare le altre ' + (items.length - MAX_RESULTS) + '.</div>';
      }
    }
    grid.innerHTML = html;
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function initPicker(root) {
    if (root.dataset.iconPickerInit === '1') return;
    root.dataset.iconPickerInit = '1';

    var targetSel = root.getAttribute('data-target');
    var target = targetSel ? document.querySelector(targetSel) : null;
    var currentValue = target ? target.value : '';
    var current = parseClass(currentValue);

    var preview = root.querySelector('[data-icon-preview]');
    function refreshPreview() {
      if (!preview) return;
      var cls = buildClass(current.family, current.name);
      preview.innerHTML = cls ? '<i class="' + cls + '"></i>' : '<span style="color:var(--ec-ink-400);font-size:12px">Nessuna icona selezionata</span>';
    }

    var search = root.querySelector('input[type="search"], input.ec-icon-picker__search');
    var grid = root.querySelector('.ec-icon-picker__grid');
    if (!grid) {
      grid = document.createElement('div');
      grid.className = 'ec-icon-picker__grid';
      root.appendChild(grid);
    }

    grid.addEventListener('click', function (e) {
      var btn = e.target.closest && e.target.closest('.ec-icon-picker__cell');
      if (!btn) return;
      var cls = btn.getAttribute('data-icona') || '';
      var parsed = parseClass(cls);
      current = parsed;
      if (target) target.value = cls;
      grid.querySelectorAll('.ec-icon-picker__cell').forEach(function (c) { c.classList.remove('is-selected'); });
      btn.classList.add('is-selected');
      refreshPreview();
    });

    var debounce;
    function onSearch() {
      clearTimeout(debounce);
      debounce = setTimeout(function () {
        loadCatalog().then(function (items) { renderGrid(grid, items, search ? search.value : '', current); });
      }, 80);
    }
    if (search) search.addEventListener('input', onSearch);

    refreshPreview();
    grid.innerHTML = '<div class="ec-icon-picker__loading">Carico icone…</div>';
    loadCatalog().then(function (items) {
      renderGrid(grid, items, '', current);
    }).catch(function () {
      grid.innerHTML = '<div class="ec-icon-picker__empty">Errore nel caricamento icone.</div>';
    });
  }

  function initAll(scope) {
    var root = scope || document;
    root.querySelectorAll('.ec-icon-picker[data-searchable="1"]').forEach(initPicker);
  }

  document.addEventListener('DOMContentLoaded', function () { initAll(); });

  // Expose for late-added pickers (e.g. injected via inline JS / HTMX swap).
  window.EconIconPicker = {
    init: initAll,
    initOne: initPicker,
    parseClass: parseClass,
    buildClass: buildClass
  };

  // Re-init after HTMX swaps so pickers in newly-swapped HTML come alive.
  document.body.addEventListener('htmx:afterSwap', function (ev) {
    initAll(ev.target || document);
  });
})();
