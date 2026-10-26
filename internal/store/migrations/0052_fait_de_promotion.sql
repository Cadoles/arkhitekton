-- +goose Up
-- La promotion est un fait du plugin Mstore (doc 16 §16.7). Les réactions déclarées
-- sur l'ancien type le suivent ; les faits déjà écrits restent tels quels.
update reactions set event_type = 'mstore.release.promoted' where event_type = 'release.promoted';

-- +goose Down
update reactions set event_type = 'release.promoted' where event_type = 'mstore.release.promoted';
