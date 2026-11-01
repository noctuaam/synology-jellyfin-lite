/* Folder video navigation. ES5 for the LG webOS Chrome 53 client. */
(function () {
    'use strict';
    var scheduled = false;
    var detailKey = '';
    var detailPage = null;
    var detailLoading = false;
    var detailNotVideo = false;
    var generation = 0;
    var playlistKey = '';
    var playlist = [];
    var playlistLoading = false;

    var focusStyle = document.createElement('style');
    focusStyle.textContent = '.nasVideoNeighbor{background:#26384a;color:#eef6ff;border:2px solid transparent;}.nasVideoNeighbor:focus,.nasVideoNeighbor.focused{background:#eef6ff!important;color:#101820!important;border-color:#55d6ff!important;outline:3px solid #55d6ff!important;outline-offset:3px;box-shadow:0 0 0 5px rgba(85,214,255,.25);}.nasVideoNeighbor:disabled{background:#26384a!important;color:#a6b1be!important;outline:none!important;box-shadow:none!important;}';
    document.head.appendChild(focusStyle);

    function neighborButton(label, item) {
        var button = document.createElement('button');
        button.type = 'button';
        button.className = 'button-flat emby-button nasVideoNeighbor';
        button.textContent = label;
        button.disabled = !item;
        button.style.cssText = 'padding:12px 18px;margin:4px 8px 4px 0;border-radius:6px;';
        if (!item) button.style.opacity = '0.35';
        button.title = item ? label + ': ' + item.Name : label + ': конец папки';
        button.setAttribute('aria-label', button.title);
        if (item) {
            button.addEventListener('click', function () {
                window.location.hash = '/details?id=' + encodeURIComponent(item.Id) + '&serverId=' + encodeURIComponent(item.ServerId);
            });
        }
        return button;
    }

    function updateDetail(bridge) {
        var match = /^#\/details\?/.test(window.location.hash) && /[?&]id=([^&]+)/i.exec(window.location.hash);
        if (!match) {
            detailKey = '';
            detailPage = null;
            detailLoading = false;
            generation++;
            return;
        }
        // Jellyfin keeps old detail views in the DOM, including their IDs.
        var pages = document.querySelectorAll('.itemDetailPage');
        var page = null;
        for (var p = 0; p < pages.length; p++) {
            if (pages[p].getClientRects().length) page = pages[p];
        }
        if (!page) return;
        var id = decodeURIComponent(match[1]);
        if (detailKey === id && detailPage === page && (detailLoading || detailNotVideo || page.querySelector('.nasVideoNeighbors'))) return;
        detailKey = id;
        detailPage = page;
        detailLoading = true;
        detailNotVideo = false;
        var requestGeneration = ++generation;
        var old = page.querySelector('.nasVideoNeighbors');
        if (old) old.parentNode.removeChild(old);
        bridge.load(id).then(function (result) {
            if (requestGeneration !== generation || !document.documentElement.contains(page)) return;
            detailLoading = false;
            if (result.item.MediaType !== 'Video') {
                detailNotVideo = true;
                return;
            }
            var items = result.items;
            var index = -1;
            for (var i = 0; i < items.length; i++) {
                if (items[i].Id === id) index = i;
            }
            if (index < 0) return;
            var actions = page.querySelector('.mainDetailButtons');
            if (!actions) return;
            var navigation = document.createElement('div');
            navigation.className = 'nasVideoNeighbors focuscontainer-x';
            navigation.setAttribute('role', 'group');
            navigation.setAttribute('aria-label', 'Видео в этой папке');
            navigation.appendChild(neighborButton('← Предыдущий файл', items[index - 1]));
            navigation.appendChild(neighborButton('Следующий файл →', items[index + 1]));
            actions.parentNode.insertBefore(navigation, actions.nextSibling);
        }).catch(function (error) {
            if (requestGeneration === generation) detailLoading = false;
            console.warn('Folder video navigation unavailable', error);
        });
    }

    function updatePlayer(bridge) {
        var manager = bridge.manager;
        if (!/^#\/video/.test(window.location.hash)) {
            playlistKey = '';
            playlist = [];
            return;
        }
        var item = currentItem(manager);
        if (!item || item.MediaType !== 'Video') return;
        var previous = document.querySelector('#videoOsdPage .btnPreviousTrack');
        var next = document.querySelector('#videoOsdPage .btnNextTrack');
        if (!previous || !next) return;
        if ((playlistKey !== item.Id || playlist.length < 2) && !playlistLoading) {
            playlistLoading = true;
            Promise.resolve(manager.getPlaylist()).then(function (items) {
                playlistLoading = false;
                playlistKey = item.Id;
                playlist = items || [];
                applyPlayerButtons();
            }).catch(function () { playlistLoading = false; });
        }
        applyPlayerButtons();

        function applyPlayerButtons() {
            var current = currentItem(manager);
            if (!current || current.Id !== playlistKey) return;
            var index = manager.getCurrentPlaylistIndex();
            previous.disabled = index <= 0;
            next.disabled = index < 0 || index >= playlist.length - 1;
            previous.title = 'Предыдущий файл (Shift+P)';
            next.title = 'Следующий файл (Shift+N)';
            previous.setAttribute('aria-label', 'Предыдущий файл');
            next.setAttribute('aria-label', 'Следующий файл');
            if (playlist.length > 1) {
                previous.classList.remove('hide');
                next.classList.remove('hide');
            }
        }
    }

    function currentItem(manager) {
        var player = manager.getCurrentPlayer();
        return player ? manager.currentItem(player) : null;
    }

    function schedule() {
        if (scheduled) return;
        scheduled = true;
        window.setTimeout(function () {
            scheduled = false;
            var bridge = window.NASVideoBridge;
            if (!bridge) return;
            updateDetail(bridge);
            updatePlayer(bridge);
        }, 40);
    }

    window.addEventListener('hashchange', schedule);
    document.addEventListener('viewshow', schedule, true);
    new MutationObserver(schedule).observe(document.documentElement, { childList: true, subtree: true });
    schedule();
}());
