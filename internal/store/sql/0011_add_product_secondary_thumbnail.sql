-- +migrate Up
alter table product
    add column secondary_thumbnail_id int null after thumbnail_id;

alter table product
    add index idx_product_secondary_thumbnail_id (secondary_thumbnail_id);

alter table product
    add constraint fk_product_secondary_thumbnail_media
        foreign key (secondary_thumbnail_id) references media(id);

-- +migrate Down
alter table product
    drop foreign key fk_product_secondary_thumbnail_media;

alter table product
    drop index idx_product_secondary_thumbnail_id;

alter table product
    drop column secondary_thumbnail_id;

