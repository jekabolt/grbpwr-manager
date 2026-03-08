-- +migrate Up

-- Add product_name to bq_size_confidence for display (product_id + product_name instead of SKU)
alter table bq_size_confidence
    add column product_name varchar(255) null after product_id;

-- +migrate Down

alter table bq_size_confidence
    drop column product_name;
