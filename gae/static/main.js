function reloadMessages() {
	$.get('/channels/test?from=' + new Date(new Date().getTime() - 10 * 60000).toJSON(), function(data) {
		$('#last-messages').empty();
		for (var i = 0; i < data.length; i++) {
			$('#last-messages').append('<dt>' + data[i].CreatedAt + '</dt><dd>' + atob(data[i].Payload) + '</dd>');
		}
	});
}

if ('serviceWorker' in navigator) {
	console.log('Service Worker is supported.');
	navigator.serviceWorker.register('sw.js').then(function(reg) {
		console.log(reg);
		reg.pushManager.subscribe({
			userVisibleOnly: true
		}).then(function(sub) {
			console.log('Sub:', sub);
			$('#gcm-subscribing-status').html('Yes');
			var parts = sub.endpoint.split(/\//);
			var iid = parts[parts.length-1];
			$('#gcm-iid').html(iid);
			var subDef = JSON.stringify({
				Channel: 'test',
				IID: iid,
			});
			$.post('/channels/test/subscribing', subDef, function(data) {
				var subscribing = false;
				if (data.length > 0) {
					subscribing = true;
				}
				$('#open-messaging-toggle-subscribing').html('' + subscribing);
				$('#open-messaging-toggle-subscribing').bind('click', function() {
					var url = '/channels/test/subscribe';
					if (subscribing) {
						url = '/channels/test/unsubscribe';
					}
					$.post(url, subDef, function(data) {
						subscribing = !subscribing;
						$('#open-messaging-toggle-subscribing').html('' + subscribing);
					});
				});
			});
			navigator.serviceWorker.addEventListener('message', function(event){
				console.log("Received Message: " + event);
				reloadMessages();
			});
		});
	}).catch(function(err) {
		console.log(err);
	});
} else {
	alert("Service Worker not supported in browser, example won't work.");
}

reloadMessages();
