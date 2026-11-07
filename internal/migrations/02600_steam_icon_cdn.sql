-- +goose Up
-- Steam serves recent achievement icons only from its current CDN path.
update achievements
set icon_url = 'https://shared.fastly.steamstatic.com/community_assets/images/apps/'
               || substr(icon_url, length('https://steamcdn-a.akamaihd.net/steamcommunity/public/images/apps/') + 1)
where icon_url like 'https://steamcdn-a.akamaihd.net/steamcommunity/public/images/apps/%';
